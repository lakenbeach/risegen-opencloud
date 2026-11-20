#!/bin/bash
set -e

###############################################################################
# submit-patch.sh - Automate PR submission to upstream Go CDK repository
#
# Parameterized version: accepts --fork, --branch, --target-repo as arguments.
#
# Prerequisites:
#   1. GitHub CLI (gh) installed and authenticated
#   2. Fork of google/go-cloud with feature branch pushed
#   3. All tests passing locally
#
# Usage:
#   ./scripts/submit-patch.sh --branch feat/my-feature --title "feat: description"
#   ./scripts/submit-patch.sh --fork myuser --branch fix/bug --target-repo google/go-cloud
###############################################################################

# Defaults
TARGET_REPO="google/go-cloud"
BASE_BRANCH="main"
PR_BODY_FILE=".pr-description.md"
FORK_USERNAME=""
BRANCH_NAME=""
PR_TITLE=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --fork)
            FORK_USERNAME="$2"
            shift 2
            ;;
        --branch)
            BRANCH_NAME="$2"
            shift 2
            ;;
        --target-repo)
            TARGET_REPO="$2"
            shift 2
            ;;
        --title)
            PR_TITLE="$2"
            shift 2
            ;;
        --body)
            PR_BODY="$2"
            shift 2
            ;;
        --base)
            BASE_BRANCH="$2"
            shift 2
            ;;
        -h|--help)
            echo "Usage: $0 --branch <branch> --title <title> [--fork <username>] [--target-repo <repo>] [--base <branch>] [--body <text>]"
            echo ""
            echo "Options:"
            echo "  --branch      Feature branch name (required)"
            echo "  --title       PR title (required)"
            echo "  --fork        GitHub username of fork owner (default: auto-detect via gh api)"
            echo "  --target-repo Target repository (default: google/go-cloud)"
            echo "  --base        Base branch for PR (default: main)"
            echo "  --body        PR description text (optional)"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Validate required arguments
if [ -z "$BRANCH_NAME" ]; then
    echo "Error: --branch is required"
    exit 1
fi

if [ -z "$PR_TITLE" ]; then
    echo "Error: --title is required"
    exit 1
fi

# Auto-detect fork username if not provided
if [ -z "$FORK_USERNAME" ]; then
    echo "Detecting GitHub username..."
    FORK_USERNAME=$(gh api user --jq '.login' 2>/dev/null)
    if [ -z "$FORK_USERNAME" ]; then
        echo "Error: Could not detect GitHub username. Use --fork or run 'gh auth login'"
        exit 1
    fi
    echo "Detected username: $FORK_USERNAME"
fi

# Verify gh CLI is installed
if ! command -v gh &> /dev/null; then
    echo "Error: GitHub CLI (gh) is not installed"
    echo ""
    echo "Install instructions:"
    echo "  macOS: brew install gh"
    echo "  Linux: sudo apt install gh  # or dnf install gh"
    echo "  Other: https://cli.github.com/manual/installation"
    exit 1
fi

# Verify authentication
echo "Verifying GitHub authentication..."
if ! gh auth status &> /dev/null; then
    echo "Error: Not authenticated with GitHub"
    echo ""
    echo "Run: gh auth login"
    exit 1
fi
echo "Authenticated with GitHub"

# Check if branch exists
echo "Checking for branch: $BRANCH_NAME"
if ! git rev-parse --verify "$BRANCH_NAME" &> /dev/null; then
    echo "Error: Branch '$BRANCH_NAME' not found"
    echo ""
    echo "Available branches:"
    git branch -a | head -20
    exit 1
fi
echo "Branch found: $BRANCH_NAME"

# Push latest changes
echo "Pushing latest changes to fork..."
git push origin "$BRANCH_NAME"

# Check if PR already exists
echo "Checking for existing PRs..."
EXISTING_PR=$(gh pr list --repo "$TARGET_REPO" --head "$FORK_USERNAME:$BRANCH_NAME" --json number --jq '.[0].number' 2>/dev/null)

if [ -n "$EXISTING_PR" ]; then
    echo "PR already exists: #$EXISTING_PR"
    echo "URL: https://github.com/$TARGET_REPO/pull/$EXISTING_PR"
    echo ""
    echo "To update existing PR, just push more commits to your branch."
    exit 0
fi

# Create PR description
if [ -n "$PR_BODY" ]; then
    echo "$PR_BODY" > "$PR_BODY_FILE"
else
    cat > "$PR_BODY_FILE" << EOF
## Summary

$PR_TITLE

## Motivation

This patch extends the Go CDK to support features required by the RISEGen
OpenCloud multicloud abstraction layer.

## Changes

- Implemented on branch: \`$BRANCH_NAME\`
- See commit history for details

## Testing

- [ ] Unit tests added
- [ ] Integration tests added
- [ ] All existing tests pass (\`go test ./...\`)

## Checklist

- [ ] Code follows Go CDK style guidelines
- [ ] Self-review completed
- [ ] Documentation updated (godoc comments)
- [ ] Test coverage adequate (>80%)
- [ ] Backward compatible (no breaking changes)
EOF
fi

# Create the PR
echo "Creating pull request..."
echo ""
echo "Target: $TARGET_REPO"
echo "Base: $BASE_BRANCH"
echo "Head: $FORK_USERNAME:$BRANCH_NAME"
echo ""

gh pr create \
  --repo "$TARGET_REPO" \
  --title "$PR_TITLE" \
  --body-file "$PR_BODY_FILE" \
  --base "$BASE_BRANCH" \
  --head "$FORK_USERNAME:$BRANCH_NAME"

# Clean up
rm -f "$PR_BODY_FILE"

echo ""
echo "Pull request created successfully!"
echo ""
echo "Next steps:"
echo "1. Monitor PR at: https://github.com/$TARGET_REPO/pulls"
echo "2. Respond to reviewer feedback within 48 hours"
echo "3. Run CI checks if they fail"
echo "4. Once merged, wait for next release (~2-4 weeks)"
echo "5. Update go.mod to official version when released"
echo ""
echo "Documentation: docs/patch-workflow.md"
