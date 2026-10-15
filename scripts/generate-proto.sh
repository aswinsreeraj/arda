#!/usr/bin/env bash
set -euo pipefail

# Resolve repository root relative to script location
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

# Ensure Go binaries are in PATH
if command -v go >/dev/null 2>&1; then
  GOPATH_BIN="$(go env GOPATH 2>/dev/null)/bin"
  if [[ -d "${GOPATH_BIN}" ]]; then
    export PATH="${GOPATH_BIN}:${PATH}"
  fi
fi
if [[ -d "/root/go/bin" ]]; then
  export PATH="/root/go/bin:${PATH}"
fi
if [[ -d "/home/aswin/go/bin" ]]; then
  export PATH="/home/aswin/go/bin:${PATH}"
fi

# Verify required code-generation tools are installed
REQUIRED_TOOLS=("protoc" "protoc-gen-go" "protoc-gen-go-grpc")
MISSING_TOOLS=()
for tool in "${REQUIRED_TOOLS[@]}"; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    MISSING_TOOLS+=("${tool}")
  fi
done

if [[ ${#MISSING_TOOLS[@]} -gt 0 ]]; then
  echo "Error: Missing required tool(s): ${MISSING_TOOLS[*]}" >&2
  exit 1
fi

if ! command -v gofmt >/dev/null 2>&1; then
  echo "Error: Missing required tool: gofmt" >&2
  exit 1
fi

# Collect all ARDA v1 proto files
mapfile -t PROTO_FILES < <(find proto/arda/v1 -type f -name "*.proto" | sort)
if [[ ${#PROTO_FILES[@]} -eq 0 ]]; then
  echo "Error: No ARDA v1 proto files found under proto/arda/v1" >&2
  exit 1
fi

# Generate Go messages and gRPC stubs using module-aware output options
protoc \
  --proto_path=. \
  --go_out=. \
  --go_opt=module=github.com/aswinsreeraj/arda \
  --go-grpc_out=. \
  --go-grpc_opt=module=github.com/aswinsreeraj/arda \
  "${PROTO_FILES[@]}"

# Format generated Go files
gofmt -w gen/go/arda/v1
