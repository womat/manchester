#!/usr/bin/env bash
#
# Runs go fix, go vet, golangci-lint and govulncheck over every module in this
# repository.
#
# Two things make a plain "go vet ./..." at the root insufficient:
#
#   1. The target is a Raspberry Pi. encoder has a Linux-only file
#      (sleep_linux.go), and the demos drive real lines through
#      github.com/womat/gpio/rpi, which does not typecheck on macOS at all.
#      GOOS defaults to linux below so that the code that actually ships is
#      the code that gets checked.
#
#   2. Everything under demo/ is a separate module with its own go.mod, so a
#      root-level ./... never sees it. Each module is visited on its own.
#
# Usage:
#   scripts/check.sh                     apply fixes, check everything
#   scripts/check.sh --check             report pending fixes, change nothing
#   scripts/check.sh --skip=vuln         leave out a step (needs no network)
#   scripts/check.sh --goos=darwin       override the target platform
#   scripts/check.sh demo/manchester_sender  one module instead of all
#   scripts/check.sh -v                  show output of steps that passed too
#
# Exits non-zero if any step failed, or if a required tool is missing.

set -uo pipefail

readonly REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ALL_MODULES=(
	.
	demo/manchester_sender
	demo/manchester_listener
	demo/manchester_bench
)

ALL_STEPS=(fix vet lint vuln)

goos=linux
check_only=0
verbose=0
skipped_steps=""
modules=()

die() {
	printf 'check.sh: %s\n' "$1" >&2
	exit 2
}

for arg in "$@"; do
	case "$arg" in
	--check) check_only=1 ;;
	-v | --verbose) verbose=1 ;;
	--goos=*) goos="${arg#--goos=}" ;;
	--skip=*) skipped_steps="${arg#--skip=},${skipped_steps}" ;;
	-h | --help)
		# Print the header comment, from the third line up to the first line
		# that is no longer a comment. Beats hardcoding a line range that
		# silently drifts when the header is edited.
		awk 'NR<3 {next} /^#/ {sub(/^# ?/, ""); print; next} {exit}' "${BASH_SOURCE[0]}"
		exit 0
		;;
	-*) die "unknown flag: $arg (try --help)" ;;
	*) modules+=("${arg%/}") ;;
	esac
done

[ ${#modules[@]} -eq 0 ] && modules=("${ALL_MODULES[@]}")

step_skipped() {
	case ",${skipped_steps}," in
	*",$1,"*) return 0 ;;
	*) return 1 ;;
	esac
}

# Fail early and loudly on a missing tool rather than quietly checking less.
require_tool() {
	command -v "$1" >/dev/null && return 0
	printf 'check.sh: %s is not installed. Get it with:\n    %s\n' "$1" "$2" >&2
	printf '(or leave the step out: --skip=%s)\n' "$3" >&2
	exit 2
}

# Same versions as .github/workflows/ci.yml; raise both together.
step_skipped lint || require_tool golangci-lint \
	'go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0' lint
step_skipped vuln || require_tool govulncheck \
	'go install golang.org/x/vuln/cmd/govulncheck@v1.8.0' vuln

# Bold only when stdout is a terminal, so redirected output stays clean.
if [ -t 1 ]; then
	B=$(tput bold) N=$(tput sgr0) R=$(tput setaf 1) G=$(tput setaf 2) Y=$(tput setaf 3)
else
	B="" N="" R="" G="" Y=""
fi

failures=()

run_step() {
	local module="$1" step="$2"
	shift 2

	if step_skipped "$step"; then
		printf '  %-6s %sskipped%s\n' "$step" "$Y" "$N"
		return
	fi

	local output status
	output=$("$@" 2>&1)
	status=$?

	# govulncheck prints a report even when it finds nothing, so success is
	# judged by the exit status alone and its output stays hidden unless asked
	# for. golangci-lint prints its findings and exits 1; go fix -diff prints a
	# patch and exits 1. Both are failures worth showing in full.
	if [ $status -eq 0 ]; then
		printf '  %-6s %sok%s\n' "$step" "$G" "$N"
		[ $verbose -eq 1 ] && [ -n "$output" ] && printf '%s\n' "$output" | sed 's/^/      /'
		return 0
	fi

	printf '  %-6s %sFAILED%s\n' "$step" "$R" "$N"
	failures+=("$module: $step")
	[ -n "$output" ] && printf '%s\n' "$output" | sed 's/^/      /'
	return 0
}

for module in "${modules[@]}"; do
	dir="$REPO_ROOT/$module"
	[ -f "$dir/go.mod" ] || die "no go.mod in $module"

	printf '\n%s== %s%s (GOOS=%s)\n' "$B" "$module" "$N" "$goos"
	cd "$dir" || die "cannot enter $module"

	if [ $check_only -eq 1 ]; then
		run_step "$module" fix env GOOS="$goos" go fix -diff ./...
	else
		run_step "$module" fix env GOOS="$goos" go fix ./...
	fi
	run_step "$module" vet env GOOS="$goos" go vet ./...
	run_step "$module" lint env GOOS="$goos" golangci-lint run ./...
	run_step "$module" vuln env GOOS="$goos" govulncheck ./...
done

printf '\n%s== summary%s\n' "$B" "$N"
if [ ${#failures[@]} -eq 0 ]; then
	printf '  %sall checks passed%s over %d module(s)\n' "$G" "$N" "${#modules[@]}"
	exit 0
fi

printf '  %s%d failed%s:\n' "$R" "${#failures[@]}" "$N"
printf '    %s\n' "${failures[@]}"
exit 1
