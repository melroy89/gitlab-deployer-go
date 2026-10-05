#!/bin/sh
set -eu

smoke_image=${1:-artifact-deployer:smoke}
smoke_project=$(pwd -P)
smoke_dir=$(mktemp -d "$smoke_project/.docker-smoke.XXXXXX")
smoke_run=$(basename "$smoke_dir")
smoke_label="org.melroy.artifact-deployer.smoke-run=$smoke_run"

cleanup() {
    smoke_containers=$(docker ps -aq --filter "label=$smoke_label" 2>/dev/null) || smoke_containers=
    if [ -n "$smoke_containers" ]; then
        # IDs come exclusively from this run's label, never from a shared name.
        docker rm -f $smoke_containers >/dev/null 2>&1 || true
    fi
    # Go makes downloaded module directories read-only, even for their owner.
    chmod -R u+w "$smoke_dir" 2>/dev/null || true
    rm -r "$smoke_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$smoke_dir/tmp" "$smoke_dir/go" "$smoke_dir/cache"

# The shell runner needs Docker only. The candidate image supplies Go; identical
# host/container paths allow its tests to create bind mounts via the host daemon.
docker run --rm --network host \
    --label "$smoke_label" \
    --user "$(id -u):$(id -g)" \
    --group-add "$(stat -c %g /var/run/docker.sock)" \
    --mount "type=bind,src=$smoke_project,dst=$smoke_project" \
    --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
    --mount "type=bind,src=$(command -v docker),dst=/usr/local/bin/docker,readonly" \
    --workdir "$smoke_project" \
    --env "ARTIFACT_DEPLOYER_TEST_IMAGE=$smoke_image" \
    --env "ARTIFACT_DEPLOYER_SMOKE_RUN=$smoke_run" \
    --env "TMPDIR=$smoke_dir/tmp" \
    --env "GOPATH=$smoke_dir/go" \
    --env "GOCACHE=$smoke_dir/cache" \
    --env GOTOOLCHAIN=local \
    --entrypoint go "$smoke_image" \
    test -tags smoke -race -run '^TestDockerSmoke$' -count=1 -v .
