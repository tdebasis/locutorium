#!/usr/bin/env python3
"""Report coverage of the lines a pull request adds.

Go's coverage profile (coverage.out) records blocks as
    <file>:<startLine>.<startCol>,<endLine>.<endCol> <numStmt> <count>
per line, which is not a format diff-cover (or anything else off the
shelf) understands. This script reads that profile plus a unified diff
of the pull request and computes what fraction of the lines the diff
*adds* are covered, without any third-party dependency.

Usage:
    new-lines-coverage.py <coverage.out> [diff-file]   # diff-file defaults to stdin
    new-lines-coverage.py --self-test

Prints exactly one line to stdout: `new=<pct>` (one decimal) or
`new=n/a` when there is nothing to divide by. With --verbose, also
prints per-file covered/uncovered/not-counted counts to stderr.
"""

import re
import sys

PROFILE_LINE_RE = re.compile(
    r"^(?P<file>.+):(?P<sl>\d+)\.(?P<sc>\d+),(?P<el>\d+)\.(?P<ec>\d+) "
    r"(?P<nstmt>\d+) (?P<cnt>\d+)$"
)
HUNK_HEADER_RE = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@")


def read_module_prefix(go_mod_path="go.mod"):
    """Read the module path from go.mod's `module` line.

    Go coverage profiles name files as `<module>/<relative/path>.go`,
    but a diff names them by their repo-relative path. Stripping the
    module prefix (plus the separating slash) is what lines the two
    up; matching on the path SUFFIX after that prefix, rather than on
    the whole string, is what makes it work regardless of anything
    the profile prepends beyond the module path itself.
    """
    with open(go_mod_path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line.startswith("module "):
                return line[len("module "):].strip()
    raise ValueError(f"no 'module' line found in {go_mod_path}")


def parse_profile(text):
    """Return {file: [(startLine, endLine, count), ...]}, skipping the mode line."""
    blocks = {}
    for line in text.splitlines():
        if not line or line.startswith("mode:"):
            continue
        m = PROFILE_LINE_RE.match(line)
        if not m:
            continue
        blocks.setdefault(m.group("file"), []).append(
            (int(m.group("sl")), int(m.group("el")), int(m.group("cnt")))
        )
    return blocks


def parse_diff(text):
    """Return {file: [added_line_number, ...]} for a unified (-U0) diff.

    Only '+' lines carry meaning here: with no context lines, every
    line inside a hunk is either added or removed, and only added
    lines occupy positions in the new (HEAD) file that a coverage
    profile of HEAD can speak to.
    """
    added = {}
    current_file = None
    new_lineno = None
    for line in text.splitlines():
        if line.startswith("+++ "):
            path = line[4:].strip()
            if path == "/dev/null":
                current_file = None
            else:
                # strip a single leading "b/" (git's default diff prefix)
                current_file = path[2:] if path.startswith("b/") else path
            new_lineno = None
            continue
        if line.startswith("@@ "):
            m = HUNK_HEADER_RE.match(line)
            if m:
                new_lineno = int(m.group(1))
            continue
        if current_file is None or new_lineno is None:
            continue
        if line.startswith("+++") or line.startswith("---"):
            continue
        if line.startswith("+"):
            added.setdefault(current_file, []).append(new_lineno)
            new_lineno += 1
        elif line.startswith("-"):
            pass  # removed line: does not occupy a position in the new file
    return added


def strip_module_prefix(profile_file, module_prefix):
    """Return profile_file's path relative to the module, or None if it
    does not live under module_prefix."""
    prefix = module_prefix.rstrip("/") + "/"
    if profile_file.startswith(prefix):
        return profile_file[len(prefix):]
    return None


def classify(profile_blocks, module_prefix, added_lines):
    """Classify each added line as covered/uncovered/not-counted.

    Returns (covered, uncovered, per_file) where per_file maps
    relative path -> (covered, uncovered, not_counted) for --verbose.
    """
    # Index profile blocks by their repo-relative path (suffix after the
    # module prefix), so they can be looked up by the diff's own paths.
    by_relpath = {}
    for profile_file, blocks in profile_blocks.items():
        rel = strip_module_prefix(profile_file, module_prefix)
        if rel is not None:
            by_relpath.setdefault(rel, []).extend(blocks)

    total_covered = 0
    total_uncovered = 0
    per_file = {}

    for path, lines in added_lines.items():
        if path.endswith("_test.go"):
            continue
        blocks = by_relpath.get(path)
        if blocks is None:
            continue  # file not in the profile (e.g. non-Go, or untested package)
        f_covered = f_uncovered = f_not_counted = 0
        for lineno in lines:
            spanning = [c for (sl, el, c) in blocks if sl <= lineno <= el]
            if not spanning:
                f_not_counted += 1
            elif any(c > 0 for c in spanning):
                f_covered += 1
            else:
                f_uncovered += 1
        total_covered += f_covered
        total_uncovered += f_uncovered
        per_file[path] = (f_covered, f_uncovered, f_not_counted)

    return total_covered, total_uncovered, per_file


def format_pct(covered, uncovered):
    denom = covered + uncovered
    if denom == 0:
        return "n/a"
    return f"{(covered / denom) * 100:.1f}"


def run(profile_path, diff_text, verbose, go_mod_path="go.mod"):
    module_prefix = read_module_prefix(go_mod_path)
    with open(profile_path, "r", encoding="utf-8") as f:
        profile_text = f.read()
    profile_blocks = parse_profile(profile_text)
    added_lines = parse_diff(diff_text)
    covered, uncovered, per_file = classify(profile_blocks, module_prefix, added_lines)

    if verbose:
        if not per_file:
            print("new-lines-coverage: no added, in-profile Go lines", file=sys.stderr)
        for path, (c, u, n) in sorted(per_file.items()):
            print(
                f"new-lines-coverage: {path}: covered={c} uncovered={u} not_counted={n}",
                file=sys.stderr,
            )

    print(f"new={format_pct(covered, uncovered)}")


# --- self-test -------------------------------------------------------------

def self_test():
    module_prefix = "example.com/mod"

    profile_text = "\n".join([
        "mode: set",
        # line 2 covered, line 4 uncovered; line 3 has no block at all
        "example.com/mod/pkg/file.go:2.1,2.10 1 1",
        "example.com/mod/pkg/file.go:4.1,4.10 1 0",
        "",
    ])

    diff_with_added_lines = "\n".join([
        "diff --git a/pkg/file.go b/pkg/file.go",
        "index 0000000..1111111 100644",
        "--- a/pkg/file.go",
        "+++ b/pkg/file.go",
        "@@ -0,0 +2,3 @@",
        "+covered statement",
        "+// a comment, not a statement",
        "+uncovered statement",
        "",
    ])

    empty_diff = ""

    failures = []

    profile_blocks = parse_profile(profile_text)
    added_lines = parse_diff(diff_with_added_lines)

    if added_lines.get("pkg/file.go") != [2, 3, 4]:
        failures.append(f"parse_diff: expected added lines [2, 3, 4], got {added_lines.get('pkg/file.go')}")

    covered, uncovered, per_file = classify(profile_blocks, module_prefix, added_lines)
    c, u, n = per_file.get("pkg/file.go", (None, None, None))
    if (c, u, n) != (1, 1, 1):
        failures.append(f"classify: expected (covered=1, uncovered=1, not_counted=1), got (covered={c}, uncovered={u}, not_counted={n})")

    pct = format_pct(covered, uncovered)
    if pct != "50.0":
        failures.append(f"format_pct: expected '50.0' for 1 covered / 1 uncovered, got {pct!r}")

    # the n/a case: no added lines at all
    empty_added = parse_diff(empty_diff)
    empty_covered, empty_uncovered, _ = classify(profile_blocks, module_prefix, empty_added)
    empty_pct = format_pct(empty_covered, empty_uncovered)
    if empty_pct != "n/a":
        failures.append(f"format_pct: expected 'n/a' for an empty diff, got {empty_pct!r}")

    if failures:
        for f in failures:
            print(f"self-test: FAILED: {f}", file=sys.stderr)
        return 1

    print("self-test: ok")
    return 0


def main(argv):
    verbose = "--verbose" in argv
    argv = [a for a in argv if a != "--verbose"]

    if "--self-test" in argv:
        return self_test()

    if len(argv) < 2:
        print("usage: new-lines-coverage.py <coverage.out> [diff-file] [--verbose]", file=sys.stderr)
        print("       new-lines-coverage.py --self-test", file=sys.stderr)
        return 2

    profile_path = argv[1]
    if len(argv) >= 3:
        with open(argv[2], "r", encoding="utf-8") as f:
            diff_text = f.read()
    else:
        diff_text = sys.stdin.read()

    run(profile_path, diff_text, verbose)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
