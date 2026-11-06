#!/bin/sh
# Deploys the shop in vhs/shop to the floci of vhs/docker-compose.yaml, dev twice and prod and
# pr-128 once, invokes its functions with the events kept for them, a few of which fail, and
# writes what cloudwatch would hold of them and floci does not, so kdst ui has a history, logs,
# invocations and metrics to show. make vhs runs it after building bin/kdst.
set -eu

cd "$(dirname "$0")/shop"
export AWS_CONFIG_FILE=../aws-config
export AWS_PROFILE=vhs
kdst=../../bin/kdst

# invoke sends an event n times, and goes on when the function fails.
invoke() {
  stage=$1
  function=$2
  event=$3
  n=$4
  while [ "$n" -gt 0 ]; do
    $kdst invoke --stage "$stage" -f "$function" --path ".koldstart/events/$function/$event.json" >/dev/null 2>&1 || true
    n=$((n - 1))
  done
}

# The ui counts writes of the state less than a minute apart as one deploy, so the second deploy
# of dev waits for the minute to pass.
first=$(date +%s)
$kdst deploy --stage dev --config koldstart.first.pkl --yes
$kdst deploy --stage prod --yes
$kdst deploy --stage pr-128 --config koldstart.first.pkl --yes

invoke prod api list-products 6
invoke prod api get-order 4
invoke prod api place-order 3
invoke prod worker jobs 2

while [ $(($(date +%s) - first)) -le 65 ]; do
  sleep 1
done
$kdst deploy --stage dev --yes

invoke dev report daily 2
invoke dev worker jobs 3
invoke dev api list-products 5
invoke dev api get-order 3
invoke dev api place-order 4
invoke dev api empty-order 1
invoke dev api missing-order 2
invoke dev api get-order 2
invoke dev api place-order 2
invoke dev api empty-order 1
invoke dev api list-products 2

go run ../cloudwatch
