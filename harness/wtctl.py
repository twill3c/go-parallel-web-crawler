#!/usr/bin/env python3
"""wtctl.py — worktree 並列 + クリーンベースライン + 差分ゲート。

Python 標準ライブラリ + git のみで動作する。仕様は WT_SPEC.md(WT-xx)。

使い方:
  python scripts/wtctl.py open  --loop loop_004 [--base main]
  python scripts/wtctl.py list
  python scripts/wtctl.py gate  [--base main]          # 差分ゲート単体(CI でも使用)
  python scripts/wtctl.py check                        # worktree 内で: ゲート + 失敗帰属
  python scripts/wtctl.py close --loop loop_004 [--force]
"""

from __future__ import annotations

import argparse
import datetime as dt
import fnmatch
import json
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path

JST = dt.timezone(dt.timedelta(hours=9))
BASELINE_FILE = ".wt-baseline.json"

DEFAULT_CONFIG = {
    # 既定は空にする。**誤った既定は空欄より悪い** —— pytest 固定の既定を配ったために、
    # TS / R / Rust のプロジェクトで「テストが 1 件も走らないのにベースライン赤」が
    # 通っていた(HC-063)。空なら infer_* が実物から埋める。
    "base_branch": None,
    "test_command": None,
    "setup_command": None,
    "gate": {
        "max_total_lines": 500,
        "max_files": 30,
        "exempt": ["logs/loops/*", "out/*", "*.lock", "docs/generated/*"],
    },
    "secret_scan": True,
}

SECRET_BLOCK = [
    (r"-----BEGIN (RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----", "秘密鍵ヘッダ"),
    (r"AKIA[0-9A-Z]{16}", "AWS アクセスキー"),
    (r"(?i)\b(api[_-]?key|secret|token|password|passwd)\b\s*[=:]\s*['\"][^'\"]{8,}['\"]", "資格情報の直書き"),
]
SECRET_WARN = [
    (r"\beval\s*\(", "eval() の使用"),
    (r"(?i)(execute|cursor\.execute)\s*\(\s*[\"'].*%s.*[\"']\s*%", "SQL 文字列連結の疑い"),
]


def git(args: list[str], cwd: Path | None = None, check: bool = True) -> str:
    r = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    if check and r.returncode != 0:
        raise SystemExit(f"git {' '.join(args)} 失敗:\n{r.stderr.strip()}")
    return r.stdout


def repo_root(cwd: Path | None = None) -> Path:
    return Path(git(["rev-parse", "--show-toplevel"], cwd=cwd).strip())


def load_config(root: Path) -> dict:
    path = root / ".wt" / "gate.json"
    cfg = json.loads(json.dumps(DEFAULT_CONFIG))  # deep copy
    if path.exists():
        user = json.loads(path.read_text(encoding="utf-8"))
        for k, v in user.items():
            if isinstance(v, dict) and isinstance(cfg.get(k), dict):
                cfg[k].update(v)
            else:
                cfg[k] = v
    return cfg


def infer_test_command(root: Path) -> str | None:
    """プロジェクトの実物からテストコマンドを推定する。推定できなければ None。"""
    pkg = root / "package.json"
    if pkg.exists():
        try:
            if "test" in (json.loads(pkg.read_text(encoding="utf-8")).get("scripts") or {}):
                return "npm test"
        except (json.JSONDecodeError, OSError):
            pass
    if (root / "Cargo.toml").exists():
        return "cargo test"
    # Rust を下位ディレクトリに置く構成(フリートでは rust/ や crates/ が実在する)
    for manifest in sorted(root.glob("*/Cargo.toml")):
        return f"cargo test --manifest-path {manifest.parent.name}/Cargo.toml"
    # R + testthat。tests/testthat/ を直接指す —— フリートの R プロジェクトは
    # tests/*.R の入口を置かない形で揃っている(実測 4 件)
    if (root / "DESCRIPTION").exists() and (root / "tests" / "testthat").is_dir():
        return "Rscript -e \"testthat::test_dir('tests/testthat')\""
    if any((root / n).exists() for n in ("pyproject.toml", "requirements.txt", "setup.py")):
        return "python -m pytest -q --tb=no"
    if list(root.glob("tests/*.py")) or list(root.glob("test_*.py")):
        return "python -m pytest -q --tb=no"
    return None


def infer_setup_command(root: Path) -> str | None:
    """依存の導入コマンドを推定する(WT-02e)。

    新しい worktree には node_modules も vendor も無い。入れずにテストを回すと、
    実行系が起動できず**偽の赤ベースライン**になる —— それを本物の赤と見分けられないことが
    HC-063 の本体だった。入れられるものは入れてから測る。
    """
    if (root / "package-lock.json").exists():
        return "npm ci"
    if (root / "package.json").exists():
        return "npm install"
    if (root / "Gemfile").exists():
        return "bundle install"
    return None


def infer_base_branch(root: Path) -> str | None:
    """既定ブランチを git から読む。origin/HEAD → main → master → 現在のブランチ。"""
    head = git(["symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"], cwd=root, check=False).strip()
    prefix = "refs/remotes/origin/"
    if head.startswith(prefix):
        return head[len(prefix):]
    for name in ("main", "master"):
        if git(["rev-parse", "--verify", "--quiet", name], cwd=root, check=False).strip():
            return name
    return git(["rev-parse", "--abbrev-ref", "HEAD"], cwd=root, check=False).strip() or None


def rmdir_if_empty(path: Path, attempts: int = 5) -> bool:
    """空ディレクトリを畳む。Windows では直後に失敗するので少し待って試し直す。

    `git worktree remove` の直後はハンドルが残っていて `rmdir` が拒否される。
    一度きりの試行で握り潰すと、空のディレクトリが静かに積み上がる。
    """
    for i in range(attempts):
        try:
            if not path.is_dir():
                return True
            if any(path.iterdir()):
                return False
            path.rmdir()
            return True
        except OSError:
            time.sleep(0.1 * (i + 1))
    return False


def branch_exists(root: Path, name: str) -> bool:
    return bool(git(["rev-parse", "--verify", "--quiet", name], cwd=root, check=False).strip())


def resolve_config(root: Path, cfg: dict) -> dict:
    """空欄を実物から埋める。埋まらなければその場で落とす(黙って誤った既定に落ちない)。

    **空欄だけでなく「書いてあるが誤っている」も拾う。** 誤配された既定値は空欄ではないので、
    空欄だけを見ていると届かない —— 雛形が `main` を配ったが HEAD が `master` の
    プロジェクトが実測 10 件あった(HC-063)。
    """
    if not cfg.get("test_command"):
        cfg["test_command"] = infer_test_command(root)
    if not cfg.get("base_branch"):
        cfg["base_branch"] = infer_base_branch(root)
    elif not branch_exists(root, cfg["base_branch"]):
        guess = infer_base_branch(root)
        print(f'  ⚠ .wt/gate.json の base_branch "{cfg["base_branch"]}" は存在しません。'
              f'"{guess}" で進めます(gate.json を直してください)')
        cfg["base_branch"] = guess
    if not cfg.get("test_command"):
        raise SystemExit(
            'test_command を決められません。.wt/gate.json の "test_command" に、'
            "このプロジェクトのテストを走らせるコマンドを書いてください(WT-02a / HC-063)"
        )
    if not cfg.get("base_branch"):
        raise SystemExit('base_branch を決められません。.wt/gate.json の "base_branch" に書いてください')
    return cfg


# テストが「実際に走った」ことの証跡。件数を報告しない実行系は、走ったと見なさない。
RAN_EVIDENCE = [
    r"\b\d+\s+(?:passed|failed|error|errors|skipped)\b",       # pytest / vitest / jest
    r"Tests?\s+\d+",                                            # vitest の要約行
    r"test result:\s*(?:ok|FAILED)",                            # cargo test
    r"\b\d+\s+(?:tests?|examples?),\s*\d+\s+(?:failures?|assertions?)",  # rspec / minitest
]
# 「走らなかった」と出力自身が言っている
NOT_RAN_PHRASES = [
    "no tests ran", "no tests were found", "no test files found",
    "no tests found", "missing script", "no test specified",
]
# 実行系そのものが起動できていない
LAUNCH_FAILURE = [
    "command not found", "not recognized as an internal", "は、内部コマンド",
    "no such file or directory", "cannot find module",
    "this is not the tsc command",  # npx が未導入コマンドを拾うと終了コード 0 で返る
]


ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


def strip_ansi(text: str) -> str:
    """色付けの制御コードを落とす。

    落とさないと、vitest の `\\x1b[32m20 passed` のように**数字の直前が単語文字になり**、
    `\\b\\d+` が一致しない。実際に「20 passed」と出ているのに証跡なしと判定した
    (合成フィクスチャでは再現しない —— 素の文字列には制御コードが無いため)。
    """
    return ANSI.sub("", text)


def judge_ran(cmd: str, r: subprocess.CompletedProcess) -> tuple[bool, str]:
    """テストが実際に走ったか。走っていなければ理由を返す(WT-02a / HC-063)。

    **「実行できなかった」と「失敗が無かった」を同じ顔で報告しない**ことが、この関数の全部である。
    終了コードだけを信じない —— 何も実行せずに 0 を返す経路が実在する。
    """
    out = strip_ansi(f"{r.stdout}\n{r.stderr}")
    low = out.lower()
    for phrase in LAUNCH_FAILURE:
        if phrase in low:
            return False, f"実行系を起動できていない({phrase})"
    for phrase in NOT_RAN_PHRASES:
        if phrase in low:
            return False, f"テストが 1 件も走っていない({phrase})"
    if r.returncode == 5 and "pytest" in cmd:
        return False, "pytest 終了コード 5 — 収集されたテストが 0 件"
    if r.returncode == 127:
        return False, "終了コード 127 — コマンドが見つからない"
    if not any(re.search(p, out) for p in RAN_EVIDENCE) and r.returncode == 0:
        return False, "成功を返しているが、実行件数の証跡が出力に無い"
    return True, ""


def run_tests(cmd: str, cwd: Path, assume_ran: bool = False) -> dict:
    """テストを実行し、走ったか / 緑赤 / 失敗テスト ID(pytest の場合)を返す。"""
    r = subprocess.run(cmd, shell=True, cwd=cwd, capture_output=True, text=True)
    failed_ids = sorted(set(re.findall(r"^FAILED\s+(\S+)", r.stdout, re.MULTILINE)))
    tail = "\n".join((r.stdout.strip().splitlines() or [""])[-3:])
    ran, why = judge_ran(cmd, r)
    if assume_ran and not ran:
        # 緩める側だけを置かない。使うたびに大声で言う(AGENTS.md 品質ゲート)
        print(f"  ⚠ assume_ran により「走った」と見なしています。本来の判定: {why}")
        ran, why = True, ""
    return {
        "command": cmd,
        "returncode": r.returncode,
        "ran": ran,
        "not_ran_reason": why,
        "green": ran and r.returncode == 0,
        "failed_ids": failed_ids,
        "summary": tail,
        "ts": dt.datetime.now(JST).isoformat(timespec="seconds"),
    }


# ---------------------------------------------------------------- open

def worktrees_dir(root: Path) -> Path:
    return root.parent / f"{root.name}.worktrees"


def cmd_open(args) -> int:
    root = repo_root()
    cfg = resolve_config(root, load_config(root))
    base = args.base or cfg["base_branch"]
    git(["rev-parse", "--verify", base])  # ベース存在確認
    branch = f"loop/{args.loop}"
    path = worktrees_dir(root) / args.loop
    if path.exists():
        raise SystemExit(f"既に存在します: {path}(WT-01a: 1 ループ 1 worktree)")
    path.parent.mkdir(parents=True, exist_ok=True)
    git(["worktree", "add", str(path), "-b", branch, base], cwd=root)
    print(f"worktree 作成: {path}(ブランチ {branch} ← {base})")

    setup = cfg.get("setup_command")
    if not setup:
        setup = infer_setup_command(path)
        if setup:
            print(f'  setup_command が空なので "{setup}" と見なします(.wt/gate.json で上書きできます)')
    if setup:
        # WT-02e: 依存インストール等をベースライン測定より前に実行する。
        # これを行わないと Node 等のプロジェクトでは常に偽の赤ベースラインになる(HC-001)
        print(f"セットアップ実行中(WT-02e): {setup}")
        r = subprocess.run(setup, shell=True, cwd=path, capture_output=True, text=True)
        if r.returncode != 0:
            tail = "\n".join(((r.stderr or r.stdout).strip().splitlines() or [""])[-5:])
            print(f"  ✗ setup_command 失敗(exit {r.returncode}):\n{tail}")
            print(f"  ベースラインは未測定です。原因解消後に open をやり直すか、"
                  f"`close --loop {args.loop} --force` で worktree を破棄してください")
            return 1

    print(f"クリーンベースライン測定中(WT-02a): {cfg['test_command']}")
    baseline = run_tests(cfg["test_command"], path, cfg.get("assume_ran", False))
    baseline["base_branch"] = base
    baseline["base_commit"] = git(["rev-parse", "--short", base], cwd=root).strip()

    if not baseline["ran"]:
        # WT-02a / HC-063: 実行できなかったことを「赤」として記録してはならない。
        # 記録すると、以後の check で既存失敗 0 件として扱われ、失敗帰属が全件エージェントに寄る。
        print(f"  ✗ テストが走っていません: {baseline['not_ran_reason']}")
        print(f"    コマンド: {cfg['test_command']}")
        print(f"    出力の末尾: {baseline['summary'] or '(なし)'}")
        guess = infer_test_command(path)
        if guess and guess != cfg["test_command"]:
            print(f'    このプロジェクトの実物からは "{guess}" と見えます')
        print('    .wt/gate.json の "test_command" を直してから open をやり直してください')
        print("    (実行系が件数を報告しない場合に限り \"assume_ran\": true で先へ進めます)")
        git(["worktree", "remove", "--force", str(path)], cwd=root, check=False)
        git(["branch", "-D", branch], cwd=root, check=False)
        for leftover in (path, path.parent):
            rmdir_if_empty(leftover)
        print(f"    作りかけの worktree とブランチ {branch} は破棄しました")
        return 1

    (path / BASELINE_FILE).write_text(
        json.dumps(baseline, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    if baseline["green"]:
        print(f"  ✓ ベースライン緑({baseline['summary']})— エージェントに引き渡し可能")
    else:
        print(f"  ⚠ ベースライン赤: 既存失敗 {len(baseline['failed_ids'])} 件を記録しました(WT-02b)")
        for fid in baseline["failed_ids"][:10]:
            print(f"      - {fid}")
        print("    以後の check では、この既存失敗はエージェントに帰属しません")
    print(f"次: cd {path} でループを開始(loop_start の記録を忘れずに)")
    return 0


# ---------------------------------------------------------------- gate

def numstat(base_ref: str, root: Path) -> list[tuple[int, int, str]]:
    mb = git(["merge-base", base_ref, "HEAD"], cwd=root).strip()
    out = git(["diff", "--numstat", mb], cwd=root)
    rows = []
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) != 3:
            continue
        a, d, f = parts
        rows.append((0 if a == "-" else int(a), 0 if d == "-" else int(d), f))
    return rows


def added_lines(base_ref: str, root: Path) -> list[tuple[str, int, str]]:
    mb = git(["merge-base", base_ref, "HEAD"], cwd=root).strip()
    out = git(["diff", "--unified=0", mb], cwd=root)
    result, fname, lineno = [], "?", 0
    for line in out.splitlines():
        if line.startswith("+++ b/"):
            fname = line[6:]
        elif line.startswith("@@"):
            m = re.search(r"\+(\d+)", line)
            lineno = int(m.group(1)) if m else 0
        elif line.startswith("+") and not line.startswith("+++"):
            result.append((fname, lineno, line[1:]))
            lineno += 1
    return result


def run_gate(root: Path, cfg: dict, base_ref: str) -> tuple[bool, list[str]]:
    msgs: list[str] = []
    ok = True
    exempt = cfg["gate"]["exempt"]

    def is_exempt(f: str) -> bool:
        return any(fnmatch.fnmatch(f, pat) for pat in exempt)

    rows = numstat(base_ref, root)
    counted = [(a, d, f) for a, d, f in rows if not is_exempt(f)]
    total = sum(a + d for a, d, _ in counted)
    nfiles = len(counted)
    exempted = len(rows) - nfiles
    limit_l = cfg["gate"]["max_total_lines"]
    limit_f = cfg["gate"]["max_files"]

    msgs.append(f"差分: {total} 行 / {nfiles} ファイル(免除 {exempted} ファイル、基準 {base_ref}→HEAD+未コミット)")
    if total > limit_l:
        ok = False
        msgs.append(f"  ✗ WT-03b: 総変更 {total} 行 > 上限 {limit_l} 行。上限を上げるのではなく PR を分割してください")
        top = sorted(counted, key=lambda r: -(r[0] + r[1]))[:5]
        for a, d, f in top:
            msgs.append(f"      {a+d:>5} 行  {f}")
    if nfiles > limit_f:
        ok = False
        msgs.append(f"  ✗ WT-03b: 変更 {nfiles} ファイル > 上限 {limit_f} ファイル")

    if cfg.get("secret_scan", True):
        hits_block, hits_warn = [], []
        for fname, lineno, text in added_lines(base_ref, root):
            if is_exempt(fname):
                continue
            for pat, label in SECRET_BLOCK:
                if re.search(pat, text):
                    hits_block.append(f"{fname}:{lineno} — {label}")
            for pat, label in SECRET_WARN:
                if re.search(pat, text):
                    hits_warn.append(f"{fname}:{lineno} — {label}")
        for h in hits_block:
            ok = False
            msgs.append(f"  ✗ WT-03d(block): {h}")
        for h in hits_warn:
            msgs.append(f"  ⚠ WT-03d(warn): {h}")

    msgs.append("ゲート: " + ("合格" if ok else "不合格"))
    return ok, msgs


def cmd_gate(args) -> int:
    root = repo_root()
    cfg = resolve_config(root, load_config(root))
    ok, msgs = run_gate(root, cfg, args.base or cfg["base_branch"])
    print("\n".join(msgs))
    return 0 if ok else 1


# ---------------------------------------------------------------- check

def cmd_check(args) -> int:
    root = repo_root()
    cfg = resolve_config(root, load_config(root))
    bl_path = root / BASELINE_FILE
    baseline = json.loads(bl_path.read_text(encoding="utf-8")) if bl_path.exists() else None
    base_ref = (baseline or {}).get("base_branch") or cfg["base_branch"]
    if baseline is not None and not git(["rev-parse", "--verify", "--quiet", base_ref], cwd=root, check=False).strip():
        # 並走中に基準ブランチが改名・削除されることがある(HC-063)
        print(f"  ⚠ ベースラインの基準ブランチ {base_ref} が見つかりません。{cfg['base_branch']} で測ります")
        base_ref = cfg["base_branch"]

    ok, msgs = run_gate(root, cfg, base_ref)
    print("\n".join(msgs))

    print(f"\nテスト再実行 + 失敗帰属(WT-02c): {cfg['test_command']}")
    current = run_tests(cfg["test_command"], root, cfg.get("assume_ran", False))
    if not current["ran"]:
        # 走っていない実行を「緑」とも「赤」とも報告しない(HC-063)
        print(f"  ✗ テストが走っていません: {current['not_ran_reason']}")
        print(f"    出力の末尾: {current['summary'] or '(なし)'}")
        print("\ncheck: 不合格 — テストを実行できていないため、失敗帰属を判定できません")
        return 1
    if baseline is not None and baseline.get("ran") is False:
        print("  ✗ ベースラインが未実行のまま記録されています。open をやり直してください(HC-063)")
        return 1
    if baseline is not None and "ran" not in baseline and not baseline.get("green") and not baseline.get("failed_ids"):
        # 旧形式。赤なのに失敗 ID が 1 件も無いのは「走っていない」の徴候である
        print("  ⚠ 旧形式のベースラインが『赤・既存失敗 0 件』です。実行できていなかった可能性があります(HC-063)")
    if baseline is None:
        print("  ⚠ ベースラインなし(wtctl open で作られていない worktree)。全失敗を表示します")
        base_failed: set[str] = set()
    else:
        base_failed = set(baseline["failed_ids"])
    cur_failed = set(current["failed_ids"])
    new_f = sorted(cur_failed - base_failed)
    pre_f = sorted(cur_failed & base_failed)
    fixed = sorted(base_failed - cur_failed)

    print(f"  現在: {'緑' if current['green'] else '赤'}({current['summary']})")
    if fixed:
        print(f"  ✓ 既存失敗の修正: {len(fixed)} 件")
    if pre_f:
        print(f"  = 既存失敗の残存(帰属しない): {len(pre_f)} 件")
    if new_f:
        ok = False
        print(f"  ✗ 新規失敗(このループの変更に帰属 — GEN-REGRESS として記録すべき): {len(new_f)} 件")
        for fid in new_f[:10]:
            print(f"      - {fid}")

    print("\ncheck: " + ("合格 — PR 作成可(WT-02d)" if ok else "不合格 — PR 作成前に解消が必要"))
    return 0 if ok else 1


# ---------------------------------------------------------------- list / close

def cmd_list(args) -> int:
    root = repo_root()
    out = git(["worktree", "list", "--porcelain"], cwd=root)
    entries, cur = [], {}
    for line in out.splitlines():
        if not line.strip():
            if cur:
                entries.append(cur)
                cur = {}
        elif " " in line:
            k, v = line.split(" ", 1)
            cur[k] = v
        else:
            cur[line] = True
    if cur:
        entries.append(cur)

    for e in entries:
        path = Path(e["worktree"])
        branch = e.get("branch", "(detached)").replace("refs/heads/", "")
        dirty = bool(git(["status", "--porcelain"], cwd=path).strip())
        bl = path / BASELINE_FILE
        blmark = "-"
        if bl.exists():
            b = json.loads(bl.read_text(encoding="utf-8"))
            blmark = "緑" if b["green"] else f"赤({len(b['failed_ids'])})"
        main_mark = "(main checkout)" if path == root and not branch.startswith("loop/") else ""
        print(f"  {branch:<24} {'dirty' if dirty else 'clean':<6} baseline:{blmark:<8} {path} {main_mark}")
    return 0


def registered_worktrees(root: Path) -> set[Path]:
    out = git(["worktree", "list", "--porcelain"], cwd=root)
    return {
        Path(line[len("worktree "):].strip()).resolve()
        for line in out.splitlines()
        if line.startswith("worktree ")
    }


def remove_worktree(root: Path, path: Path, force: bool, attempts: int = 3) -> tuple[bool, str]:
    """WT-04c: Windows のファイルロック(node_modules 等)に備えてリトライする。"""
    err = ""
    for i in range(attempts):
        r = subprocess.run(
            ["git", "worktree", "remove", "--force" if force else "--", str(path)],
            cwd=root, capture_output=True, text=True)
        if r.returncode == 0:
            return True, ""
        err = r.stderr.strip()
        if i < attempts - 1:
            time.sleep(1.0)
    return False, err


def cmd_close(args) -> int:
    root = repo_root()
    cfg = load_config(root)
    branch = f"loop/{args.loop}"
    path = worktrees_dir(root) / args.loop
    if not path.exists():
        raise SystemExit(f"worktree が見つかりません: {path}")

    branch_exists = subprocess.run(
        ["git", "rev-parse", "--verify", "--quiet", branch],
        cwd=root, capture_output=True).returncode == 0
    if not args.force:
        registered = path.resolve() in registered_worktrees(root)
        if registered and git(["status", "--porcelain"], cwd=path).strip():
            raise SystemExit(f"未コミットの変更があります: {path}(WT-04a。--force で無視)")
        if branch_exists:
            merged = subprocess.run(
                ["git", "merge-base", "--is-ancestor", branch, cfg["base_branch"]],
                cwd=root, capture_output=True).returncode == 0
            if not merged:
                raise SystemExit(f"{branch} は {cfg['base_branch']} に未マージです(WT-04a。--force で無視)")

    removed, err = remove_worktree(root, path, args.force)
    if not removed:
        subprocess.run(["git", "worktree", "prune"], cwd=root, capture_output=True)
        if path.resolve() in registered_worktrees(root):
            # 登録もディレクトリも残っている: 状態を変えずに失敗させる
            raise SystemExit(f"worktree を削除できません: {path}\n{err}")
        # 登録は解除済み(または元から未登録)。残骸の直接削除を試みる(WT-04c)
        try:
            shutil.rmtree(path)
            removed = True
        except OSError:
            pass

    subprocess.run(["git", "branch", "-D" if args.force else "-d", branch],
                   cwd=root, capture_output=True)
    if not removed:
        # 登録解除には成功し、ディレクトリ削除のみ失敗(WT-04c)。閉鎖としては成立
        print(f"⚠ WT-04c: worktree の登録は解除しましたが、ディレクトリを削除できませんでした")
        print(f"   (Windows のファイルロック等)。手動で削除してください: {path}")
        print(f"閉鎖(ディレクトリ残存): {branch}({path})")
        return 0
    # 最後の worktree を閉じたら、親の *.worktrees も空なら畳む
    rmdir_if_empty(path.parent)
    print(f"閉鎖: {branch}({path})")
    return 0


# ---------------------------------------------------------------- main

def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="worktree 並列 + 差分ゲート CLI")
    sub = p.add_subparsers(dest="cmd", required=True)

    op = sub.add_parser("open", help="ループ用 worktree を作成しベースラインを測る")
    op.add_argument("--loop", required=True)
    op.add_argument("--base", help="ベースブランチ(既定: gate.json / main)")
    op.set_defaults(func=cmd_open)

    lp = sub.add_parser("list", help="worktree 一覧と状態")
    lp.set_defaults(func=cmd_list)

    gp = sub.add_parser("gate", help="差分ゲート単体(CI でも使用)")
    gp.add_argument("--base")
    gp.set_defaults(func=cmd_gate)

    cp = sub.add_parser("check", help="worktree 内で: ゲート + テスト失敗の帰属判定")
    cp.set_defaults(func=cmd_check)

    xp = sub.add_parser("close", help="マージ済み worktree とブランチを片付ける")
    xp.add_argument("--loop", required=True)
    xp.add_argument("--force", action="store_true")
    xp.set_defaults(func=cmd_close)

    args = p.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
