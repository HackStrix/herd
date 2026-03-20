#!/bin/bash
set -e

# Restore proxy and observer to root
if [ -d "internal/proxy" ]; then
    echo "Restoring proxy..."
    mkdir -p proxy
    mv internal/proxy/* proxy/ 2>/dev/null || true
    rmdir internal/proxy
fi

if [ -d "internal/observer" ]; then
    echo "Restoring observer..."
    mkdir -p observer
    mv internal/observer/* observer/ 2>/dev/null || true
    rmdir internal/observer
fi

# Revert pool.go import back to original for observer
sed -i '' 's|"github.com/hackstrix/herd/internal/observer"|"github.com/hackstrix/herd/observer"|g' pool.go

# Create internal/core
mkdir -p internal/core

echo "Moving files to internal/core..."
FILES_TO_MOVE=(
    "process_worker_factory.go"
    "factory_cgroup_test.go"
    "sandbox.go"
    "sandbox_linux.go"
    "sandbox_unsupported.go"
    "sandbox_integration_test.go"
    "sandbox_linux_test.go"
    "registry.go"
    "ttl.go"
)

for f in "${FILES_TO_MOVE[@]}"; do
    if [ -f "$f" ]; then
        mv "$f" internal/core/
    fi
done

# Update package to 'core' in internal/core files
for f in internal/core/*.go; do
    sed -i '' 's/package herd/package core/g' "$f"
done

# Capitalize processWorker
sed -i '' 's/type processWorker/type ProcessWorker/g' internal/core/process_worker_factory.go
sed -i '' 's/\*processWorker/*ProcessWorker/g' internal/core/process_worker_factory.go
sed -i '' 's/func (w \*processWorker)/func (w *ProcessWorker)/g' internal/core/process_worker_factory.go
sed -i '' 's/&processWorker/&ProcessWorker/g' internal/core/process_worker_factory.go

# Capitalize newLocalRegistry
sed -i '' 's/func newLocalRegistry/func NewLocalRegistry/g' internal/core/registry.go

echo "✅ Core files moved and updated."
