# python

Shipping quotes in python, in [koldstart.pkl](./koldstart.pkl): `POST /quotes` checks the parcels of a request with pydantic, and answers with what sending them costs, parcel by parcel, charged by their weight or their volume, whichever is more.

## how koldstart builds it

- `quote` sets no `runtime` or `handler`: [src/quote.py](./src/quote.py) makes it `python3.13`, with the handler `src/quote.handler`. Set `runtime = "python3.12"` for the older python, or `handler = "src/other.handler"` for another file.
- the zip holds the directory of the handler's module, `src/`, so [src/pricing.py](./src/pricing.py) goes with it and is imported relative to it, `from .pricing import ...`. `tests/` is not in it. `bundle { include { "src"; "templates" } }` names what goes in when it is more than the handler's directory.
- the dependencies are [requirements.txt](./requirements.txt), beside `koldstart.pkl`, installed into the zip by uv for lambda's linux and the function's architecture, `arm64`, from any machine. pydantic's core is compiled rust, and the zip gets the build for `aarch64-manylinux2014`, whatever this machine is. A `pyproject.toml`, with its `uv.lock`, works in place of `requirements.txt`.
- `requirements.txt` is compiled from [requirements.in](./requirements.in), with the hash of every file it may install, so uv refuses a package that is not the one reviewed:

  ```bash
  uv pip compile requirements.in --universal --generate-hashes --python-version 3.13 --output-file requirements.txt
  ```

- uv installs only wheels for lambda, never building from source. A package whose newest wheels need a newer linux than `manylinux2014`, as pillow 13 does for arm64, is not found: pin a version with a `manylinux2014` wheel, or use a container image, as [container-image](../container-image) does.
- the zip holds the same bytes for the same code and dependencies on any machine, its entries dated 1980 and the scripts uv writes for this machine left out, so its hash tells koldstart whether anything changed.
- `snapStart = true` starts each instance from a snapshot taken when a version is published, rather than importing pydantic afresh, at the cost of a published version and an alias on every change.

The handler is split as a team would keep it: [src/quote.py](./src/quote.py) reads the request and answers in json, or `application/problem+json` with each field that is wrong, and [src/pricing.py](./src/pricing.py) holds the models and the prices, with the tariff handed in, so the tests price with their own. Lambda writes each line of python's `logging` as json, with the fields of `extra`, at the level `applicationLogLevel` sets.

## trying it

```bash
kdst deploy --stage development
URL="$(kdst outputs --stage development --output json | jq --raw-output .httpApiUrl)"
curl --request POST "$URL/quotes" \
  --data '{"destination": "DE", "parcels": [{"weightGrams": 2400, "lengthCentimetres": 40, "widthCentimetres": 30, "heightCentimetres": 20}]}'
```

It answers with a quote of `1745` cents: the parcel is charged as 4,800 grams, its volume, for 995 cents and 150 for each of five started kilograms. A request without parcels, or with one over 30 kilograms, is answered `400` with the fields that are wrong; one to a country the carrier does not send to, `422`.

`kdst invoke --local --stage development --function quote --path .koldstart/events/quote/quote-to-germany.json` runs the handler on this machine with uv's python 3.13 and the dependencies installed for it, against an emulator of the lambda runtime api, with the variables the stage deployed and your aws credentials, so the stage is deployed first. `kdst remove --stage development` deletes the stage.

## testing it

```bash
python3 -m venv .venv
.venv/bin/python -m pip install --quiet --disable-pip-version-check --require-hashes --requirement requirements.txt
.venv/bin/python -B -m unittest discover --start-directory tests --top-level-directory .
```

The tests import pydantic, so they run in a virtual environment with `requirements.txt` installed, by its hashes. They cover a quote, express, a body in base64, each kind of refusal, a country without service, a failure that answers `500` without saying why, and the weight a parcel is charged by.

## related

- [http-routes](../../concepts/http-routes): the routes of the stage's http api
- [defaults](../../concepts/defaults): json logs and tracing for every function at once
- [schedules](../../concepts/schedules): python on a schedule, rather than a route
