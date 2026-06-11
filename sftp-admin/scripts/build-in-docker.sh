#!/usr/bin/env bash
# Hermetic build + test for sftpadmind.
#
# Runs CMake + ctest inside gcc:13 so a Windows / macOS developer box
# can drive the Linux-only build without WSL or local toolchain setup.
# Container is single-shot (--rm); the only state that persists is the
# `build/` dir under the repo root.
#
# Usage:   ./scripts/build-in-docker.sh [extra cmake args]
# Example: ./scripts/build-in-docker.sh -DSFTPADMIN_ASAN=ON
set -euo pipefail

# Resolve the project root from this script's location, NOT $PWD, so
# the script works regardless of where it's invoked from.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
PROJECT_DIR="$(dirname -- "$SCRIPT_DIR")"

IMAGE="${SFTPADMIN_BUILD_IMAGE:-gcc:13}"
JOBS="${SFTPADMIN_BUILD_JOBS:-$(nproc 2>/dev/null || echo 4)}"

echo "==> building sftpadmind in $IMAGE (jobs=$JOBS)"
docker run --rm \
    -v "$PROJECT_DIR:/work" \
    -w /work \
    -e CMAKE_BUILD_PARALLEL_LEVEL="$JOBS" \
    "$IMAGE" \
    bash -lc '
        set -euo pipefail
        apt-get update -qq
        apt-get install -y -qq cmake git ca-certificates >/dev/null
        cmake -B build -DCMAKE_BUILD_TYPE=Debug "$@"
        cmake --build build -j "${CMAKE_BUILD_PARALLEL_LEVEL:-4}"
        ctest --test-dir build --output-on-failure
    ' bash "$@"

echo "==> done. artefacts under ./build/"
