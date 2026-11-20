#!/bin/bash
set -e

###############################################################################
# build-lib.sh - Compile the Go shared library for OpenCloud
#
# Builds libcloudadapter.so and runs Go tests.
#
# Usage:
#   ./scripts/build-lib.sh [--sign]
#
# Options:
#   --sign    Sign the output with GPG
###############################################################################

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
GO_DIR="$PROJECT_ROOT/go"
BUILD_DIR="$GO_DIR/build"
OUTPUT="$BUILD_DIR/libcloudadapter.so"
SIGN=false

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --sign)
            SIGN=true
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [--sign]"
            echo ""
            echo "Compiles the Go shared library (.so) for OpenCloud."
            echo ""
            echo "Options:"
            echo "  --sign    Sign the output with GPG"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Verify Go is installed
if ! command -v go &> /dev/null; then
    echo "Error: Go is not installed or not in PATH"
    echo "Install: https://go.dev/doc/install"
    exit 1
fi

echo "Go version: $(go version)"

# Create build directory
mkdir -p "$BUILD_DIR"

# Run Go tests
echo ""
echo "=== Running Go tests ==="
cd "$GO_DIR"
go test ./... -v -count=1
echo "Tests passed."

# Build shared library
echo ""
echo "=== Building shared library ==="
echo "Output: $OUTPUT"

CGO_ENABLED=1 go build \
    -buildmode=c-shared \
    -o "$OUTPUT" \
    ./cmd/

echo "Build complete: $OUTPUT"
ls -lh "$OUTPUT"

# Verify the library
echo ""
echo "=== Verifying library ==="
if command -v nm &> /dev/null; then
    EXPORTED=$(nm -D "$OUTPUT" 2>/dev/null | grep -c "CloudAdapter_" || true)
    echo "Exported CloudAdapter_ symbols: $EXPORTED"
fi

if command -v file &> /dev/null; then
    file "$OUTPUT"
fi

# Sign if requested
if [ "$SIGN" = true ]; then
    echo ""
    echo "=== Signing library ==="
    if ! command -v gpg &> /dev/null; then
        echo "Warning: GPG not installed, skipping signature"
    else
        gpg --detach-sign --armor "$OUTPUT"
        echo "Signed: ${OUTPUT}.asc"
    fi
fi

echo ""
echo "=== Build Summary ==="
echo "Library: $OUTPUT"
if [ -f "${OUTPUT}.asc" ]; then
    echo "Signature: ${OUTPUT}.asc"
fi
echo "Done."
