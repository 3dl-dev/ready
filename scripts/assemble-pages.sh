#!/usr/bin/env bash
# assemble-pages.sh <site-dir> <board-dist-dir> <output-dir>
#
# Builds the GitHub Pages deploy artifact for ready.3dl.dev:
#   <output-dir>/         <- <site-dir>/       (site root, unchanged)
#   <output-dir>/board/   <- <board-dist-dir>/ (ready-2f1 board build)
#
# Extracted out of .github/workflows/pages.yml (ready-2f1 rework) so the
# assemble logic can be exercised by scripts/assemble_pages_test.go instead
# of being guarded by nothing but a YAML comment.
#
# Excludes *_test.go from the copied site tree: those are ground-source
# checks for site/ content (see site/content_test.go) and were never meant
# to be served publicly. Copying them verbatim would ship Go source on the
# production site — a pre-existing issue, fixed here while touching this
# exact code path.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <site-dir> <board-dist-dir> <output-dir>" >&2
  exit 1
fi

site_dir=$1
board_dist=$2
out_dir=$3

if [[ ! -d "$site_dir" ]]; then
  echo "assemble-pages.sh: site dir not found: $site_dir" >&2
  exit 1
fi
if [[ ! -d "$board_dist" ]]; then
  echo "assemble-pages.sh: board dist dir not found: $board_dist" >&2
  exit 1
fi

mkdir -p "$out_dir"
rsync -a --exclude='*_test.go' "$site_dir"/ "$out_dir"/

mkdir -p "$out_dir/board"
rsync -a "$board_dist"/ "$out_dir/board"/
