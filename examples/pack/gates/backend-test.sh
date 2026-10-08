#!/bin/bash
# Backend suite against a THROWAWAY database on its own network. Fails closed.
# usage: backend-test.sh <absolute worktree> <short name> ["filter"]
set -uo pipefail
WT=${1:?worktree}; NAME=${2:?short name}; FILTER=${3:-}
LOG="${PIPELINE_LOGDIR:-$HOME/.cache/worktree-pipeline/logs}/backend-$NAME.log"; mkdir -p "$(dirname "$LOG")"
NET="gate-$NAME-$$"; DB="gate-db-$NAME-$$"
cleanup() { docker rm -f "$DB" >/dev/null 2>&1; docker network rm "$NET" >/dev/null 2>&1; }
trap cleanup EXIT INT TERM
docker network create "$NET" >/dev/null || exit 2
docker run -d --name "$DB" --network "$NET" -e POSTGRES_PASSWORD=test postgres:16-alpine >/dev/null || exit 2
# <wait for the DB, then run the project's test command in a container on $NET, writing to $LOG>
echo "replace this line with the project's test command" >&2; exit 2
# <parse the result: exit 1 when any test failed or when zero tests ran>
