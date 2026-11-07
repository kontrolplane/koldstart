# python-schedule

A nightly clean-up in python, in [koldstart.pkl](./koldstart.pkl): `sweep` runs at 03:00 in London and deletes the invites whose `expiresAt`, in seconds since 1970, has passed.

- `sweep` sets no `runtime` or `handler`: [src/sweep.py](./src/sweep.py) makes it `python3.13`, with the handler `src/sweep.handler`.
- `kdst.schedule("cron(0 3 * * ? *)")` is an eventbridge scheduler schedule, and its `timezone` keeps it at 03:00 through summer time. `rate(1 hour)` runs it every hour instead.
- it needs only boto3, which lambda's python has, so there is no `requirements.txt`. One beside `koldstart.pkl`, with its versions pinned, would be installed into the zip with uv.
- `link` lets it delete from the table and gives it the table's name as `INVITES_NAME`; the output `invitesTable` prints it.

## deploying it

```bash
kdst deploy
```

Then add an invite that has expired, and run the sweep now rather than at 03:00:

```bash
aws dynamodb put-item --table-name "$(kdst outputs -o json | jq -r .invitesTable)" \
  --item '{"code": {"S": "welcome"}, "expiresAt": {"N": "1000"}}'
kdst invoke -f sweep
kdst logs -f sweep
```

`invoke` prints `{"deleted": 1}`. `kdst remove` deletes the stage and the schedule, and leaves the table in place, as it holds data.
