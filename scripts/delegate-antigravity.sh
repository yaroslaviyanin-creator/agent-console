#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf '%s\n' 'Usage: delegate-antigravity.sh --task TEXT --files PATH [PATH ...] --reasoning low|medium|high [--read-only]'
}

fail() {
  printf 'delegate-antigravity: %s\n' "$1" >&2
  exit 1
}

task=''
reasoning=''
files=()
mode='accept-edits'

while (( $# > 0 )); do
  case "$1" in
    --task|--reasoning)
      [[ $# -ge 2 && -n $2 && $2 != --* ]] || fail "expected a value after $1"
      case "$1" in
        --task) task=$2 ;;
        --reasoning) reasoning=$2 ;;
      esac
      shift 2
      ;;
    --files)
      shift
      [[ $# -ge 1 && -n $1 && $1 != --* ]] || fail 'expected a path after --files'
      while (( $# > 0 )) && [[ $1 != --* ]]; do
        [[ -n $1 ]] || fail 'file paths must be nonempty'
        files+=("$1")
        shift
      done
      ;;
    --read-only) mode='plan'; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[[ -n $task ]] || fail '--task is required'
[[ ${#files[@]} -gt 0 ]] || fail '--files is required'
case "$reasoning" in
  low|medium|high) ;;
  *) fail '--reasoning must be low, medium or high' ;;
esac

command -v agy >/dev/null || fail 'agy is required on PATH'
command -v jq >/dev/null || fail 'jq is required on PATH'
repo_root=$(git rev-parse --show-toplevel) || fail 'run from inside the repository'
cd "$repo_root"

for file in "${files[@]}"; do
  [[ -e $file ]] || fail "path does not exist: $file"
done

prompt=$(
  printf 'Complete the following task in this repository. Use only the listed paths for context and edits. Preserve existing user changes.\n'
  if [[ $mode == plan ]]; then
    printf 'Perform a read-only analysis. Do not write files. Answer the task directly.\n'
  fi
  printf 'Return two short lines in the language of the task: a summary (at most two sentences) and a line labeled Changed files with a comma-separated list or the local equivalent of none. Leave out tool logs.\n\nPaths:\n'
  printf -- '- %q\n' "${files[@]}"
  printf '\nTask:\n%s\n' "$task"
)

if ! result=$(agy --model "gemini-4-hydrogen-$reasoning" --effort "$reasoning" \
    --mode "$mode" --sandbox --print-timeout 15m \
    --output-format json --print "$prompt"); then
  printf '%s\n' "$result" | jq -r '.error // empty' >&2 || true
  fail 'agy run failed'
fi

printf '%s\n' "$result" | jq -er '
  if .status == "SUCCESS" and (.response | type == "string" and length > 0)
  then (.response | sub("[[:space:]]+$"; ""))
  else error(.error // "agy did not return a response")
  end
'
