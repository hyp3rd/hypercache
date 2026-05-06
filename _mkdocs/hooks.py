"""MkDocs hooks for the HyperCache site.

Rewrites repo-relative links to source files (`../pkg/foo.go`,
`../../hypercache.go`, etc.) into canonical GitHub URLs, so the same
markdown source renders correctly both on github.com and on the
GitHub Pages MkDocs build.

Without this, the operations runbook and the RFCs reference dozens
of source files via paths like `../pkg/backend/dist_memory.go`.
GitHub renders those as in-repo links; MkDocs's strict mode flags
them as broken because `pkg/` is not part of the documentation
tree. Rewriting them at build time keeps the source markdown
GitHub-friendly while letting strict mode actually enforce
docs-internal correctness.
"""

import os
import re
from typing import Any

GITHUB_REPO_BASE = "https://github.com/hyp3rd/hypercache/blob/main"

# File extensions that we treat as "source code, not docs" — links
# to these get rewritten to GitHub URLs. .md is intentionally NOT
# in this list because doc-to-doc links should stay intra-site so
# MkDocs can validate them.
SOURCE_EXTENSIONS = (
    ".go",
    ".yaml",
    ".yml",
    ".sh",
    ".rb",
    ".txt",
    ".dockerignore",
    ".gitignore",
    ".env",
    "Dockerfile",
    "Makefile",
)

# Paths that are entire directories the docs reference for context
# (e.g. "see internal/cluster/"). These get rewritten to GitHub
# tree URLs — clicking takes the reader to a directory listing.
SOURCE_DIR_PREFIXES = (
    "pkg/",
    "internal/",
    "cmd/",
    "chart/",
    "scripts/",
    "tests/",
    "__examples/",
    ".github/",
    "docker/",
    "_mkdocs/",
)

LINK_RE = re.compile(r"\[([^\]]+)\]\(([^)]+)\)")


def _is_source_link(target: str) -> bool:
    """Return True when the link target looks like a repo source ref
    rather than an in-tree docs link."""
    # Strip anchor before extension/prefix checks.
    clean = target.split("#", 1)[0]

    # Source files by extension or basename.
    if clean.endswith(SOURCE_EXTENSIONS):
        return True

    # Directory references (no extension) that match known source
    # roots. We resolve `..` segments first so the prefix match
    # works against repo-rooted paths.
    parts = [p for p in clean.split("/") if p and p != "."]

    # Drop leading `..` segments — they all collapse to repo root
    # for our purposes (rewrite-target side).
    while parts and parts[0] == "..":
        parts.pop(0)

    if not parts:
        return False

    repo_path = "/".join(parts)
    if any(repo_path.startswith(p) for p in SOURCE_DIR_PREFIXES):
        return True

    return False


def _resolve_to_repo_root(page_src_path: str, target: str) -> str:
    """Translate a relative target into a repo-rooted path.

    Page src_path is relative to docs/ (e.g. `rfcs/0001-foo.md`).
    Target is relative to the page (e.g. `../../pkg/foo.go`). The
    returned path is relative to the repo root.
    """
    # `os.path.normpath` collapses `..` correctly; we anchor at
    # `docs/<page_dir>` and resolve from there.
    page_dir = os.path.dirname(page_src_path)
    docs_anchored = os.path.normpath(os.path.join("docs", page_dir, target))

    # The result may still start with `../` if the relative target
    # walked above the repo root (it shouldn't in practice). Trim
    # any leading `../` defensively.
    while docs_anchored.startswith("../"):
        docs_anchored = docs_anchored[3:]

    return docs_anchored


def on_page_markdown(markdown: str, page: Any, **kwargs: Any) -> str:
    """Rewrite source-code links on every page before MkDocs renders it."""
    page_src = page.file.src_path

    def replace(match: re.Match[str]) -> str:
        link_text = match.group(1)
        link_target = match.group(2)

        # Absolute URLs, mailtos, and pure anchors stay as-is.
        if link_target.startswith(("http://", "https://", "mailto:", "#")):
            return match.group(0)

        if not _is_source_link(link_target):
            return match.group(0)

        repo_path = _resolve_to_repo_root(page_src, link_target)

        # Preserve any anchor on the target (e.g. line ranges like
        # `pkg/foo.go#L34-L58`).
        if "#" in link_target and "#" not in repo_path:
            anchor = "#" + link_target.split("#", 1)[1]
            repo_path += anchor

        return f"[{link_text}]({GITHUB_REPO_BASE}/{repo_path})"

    return LINK_RE.sub(replace, markdown)
