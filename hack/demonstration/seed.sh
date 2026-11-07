#!/bin/sh
# Deploys the checkout service in hack/demonstration/checkout to floci as its team would have over a few
# days, development, staging and production each in its own steps, then invokes its functions with
# the events kept for them, a few of which fail, and has backfill write what an account would hold
# of it and floci does not: parameters, an hour of metrics, lambda's own log lines, and a deploy to
# staging github actions left unfinished. make local runs it against the floci of make emulator,
# with the local profile of hack/floci/aws-config, and make vhs against its own, with
# KOLDSTART_DEMONSTRATION_CONFIG and KOLDSTART_DEMONSTRATION_PROFILE set, both after building bin/kdst.
#
# The aws settings of the shell are never used, so it cannot deploy to a real account: the profile
# names floci, and the variables that would take precedence over it, or over the service's region,
# configuration and stages, are unset.
set -eu

cd "$(dirname "$0")/checkout"
export AWS_CONFIG_FILE="${KOLDSTART_DEMONSTRATION_CONFIG:-$(cd ../../floci && pwd)/aws-config}"
export AWS_PROFILE="${KOLDSTART_DEMONSTRATION_PROFILE:-local}"
unset AWS_DEFAULT_PROFILE AWS_ENDPOINT_URL AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_SHARED_CREDENTIALS_FILE AWS_REGION AWS_DEFAULT_REGION KOLDSTART_CONFIG KOLDSTART_STAGE
kdst=../../../bin/kdst

# deploy deploys a release to a stage as someone. koldstart names whoever deploys from github
# actions by GITHUB_ACTOR, the person whose push or merge started the run, so a deploy here
# passes as one, under that person's name, with a run of its own.
deploy() {
  who=$1
  stage=$2
  config=$3
  run=$((11873519000 + $(date +%s) % 1000))
  GITHUB_ACTIONS=true GITHUB_ACTOR="$who" GITHUB_SERVER_URL=https://github.com GITHUB_REPOSITORY=voorbeeld-winkel/checkout GITHUB_RUN_ID=$run \
    $kdst deploy --stage "$stage" --config "$config" --yes
}

# invoke sends an event to a function n times, and goes on when the function fails.
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

# say says what the seed does next, as each step can take a while.
say() {
  printf '\n\033[1m==> %s\033[0m\n' "$1"
}

# later waits for the minute since the last round of deploys to pass, as the ui counts writes of a
# state less than a minute apart as one deploy.
round=$(date +%s)
later() {
  say "waiting for the minute to pass, so the next deploys count as deploys of their own"
  while [ $(($(date +%s) - round)) -le 65 ]; do
    sleep 1
  done
  round=$(date +%s)
}

say "parameters for each stage in parameter store"
go run ../backfill parameters

# The first release: products, orders and payments. Anouk sets up staging and production by hand,
# Daan his development stage.
say "release 1 of 3: products, orders and payments"
deploy anouk-de-vries production koldstart.first.pkl
deploy anouk-de-vries staging koldstart.first.pkl
deploy daan-bakker development koldstart.first.pkl
invoke development storefront list-products 2
invoke development storefront place-order 1

# Carts and customer emails. Daan tries them on development, merges them, and the pipeline his
# merge starts ships them.
later
say "release 2 of 3: carts and customer emails"
deploy daan-bakker development koldstart.carts.pkl
deploy daan-bakker staging koldstart.carts.pkl
deploy daan-bakker production koldstart.carts.pkl

# The webhook and the nightly reconciliation, Sanne's. The pipeline her merge starts also adds
# sentry's layer to production, at a version not published to the account yet, and stops part of
# the way.
later
say "release 3 of 3: the payment provider's webhook and the nightly reconciliation, its deploy to production failing on a layer"
deploy sanne-visser development koldstart.pkl
deploy sanne-visser staging koldstart.pkl
deploy sanne-visser production koldstart.monitoring.pkl || true

# Thijs reverts the layer, and his merge deploys production again.
later
say "release 3 of 3 again, to production, without the layer"
deploy thijs-van-dam production koldstart.pkl

say "invoking production, the customers' traffic"
{
  invoke production storefront list-products 6
  invoke production storefront read-cart 3
  invoke production storefront add-to-cart 3
  invoke production storefront place-order 4
  invoke production storefront read-order 3
  invoke production storefront read-missing-order 1
  invoke production storefront read-missing-cart 1
  invoke production storefront add-unknown-product 1
  invoke production storefront place-order-from-empty-cart 1
  invoke production storefront add-malformed-item 1
  invoke production storefront list-products 2
} &
{
  invoke production payments payment-batch 3
  invoke production payments declined-card 1
  invoke production payments payment-batch 1
  invoke production payments slow-provider 1
} &
{
  invoke production notify-customers order-confirmed 2
  invoke production notify-customers order-shipped 2
  invoke production notify-customers order-disputed 1
} &
{
  invoke production stripe-webhook payment-succeeded 3
  invoke production stripe-webhook forged-signature 1
  invoke production reconcile nightly 1
} &
wait

say "invoking staging and development, the team's own requests"
{
  invoke staging storefront list-products 2
  invoke staging storefront place-order 1
  invoke staging storefront read-order 1
} &
{
  invoke staging payments payment-batch 1
  invoke staging notify-customers order-confirmed 1
  invoke staging stripe-webhook payment-succeeded 1
} &
{
  invoke development storefront list-products 1
  invoke development storefront add-to-cart 1
  invoke development storefront add-malformed-item 1
  invoke development storefront place-order 1
} &
{
  invoke development payments declined-card 1
  invoke development stripe-webhook forged-signature 1
} &
wait

say "metrics, lambda's own log lines and an unfinished deploy to staging"
go run ../backfill
say "done"
