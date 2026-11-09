# queues

A queue takes work off a function that should answer quickly and hands it to one that does it at its own pace. `kdst.queue()` is an sqs queue that keeps a message for 14 days; a producer linked `write` may send to it, and `kdst.fromQueue("jobs")` has lambda read it and hand a worker batches of messages.

```pkl
(kdst.fromQueue("jobs")) {
  batchSize = 10
  maximumBatchingWindow = 5.s
  maximumConcurrency = 2
  reportBatchItemFailures = true
}

["jobs"] = (kdst.queue()) {
  properties {
    VisibilityTimeout = functions["worker"].timeout.toUnit("s").value.toInt() * 6
  }
}
```

- `reportBatchItemFailures`, on by default, lets the worker answer with `batchItemFailures`, the messages it could not handle. Only those are delivered again; the rest of the batch is deleted.
- `maximumConcurrency` caps how many workers read the queue at once, to spare what they write to. The messages wait in the queue meanwhile.
- the visibility timeout is how long a message a worker received stays hidden from the others. Shorter than the worker's timeout, a slow batch is delivered a second time while the first is still at it, so it is worked out from the timeout here: six times, as lambda advises.
- sqs delivers each message at least once, not exactly once, so the worker stores a job with a condition, `attribute_not_exists(id)`: a message delivered twice, or a job sent twice, is one item in `processed`.

## trying it

```bash
kdst deploy --stage development
kdst invoke --stage development --function producer --path .koldstart/events/producer/sent-twice.json
kdst logs --stage development --function worker
```

The producer sends two jobs, each twice. The worker's log says `stored a job` once for each, and `the job was stored already` for the copies; `processed` holds two items. `failing-job.json` sends a job the worker fails on every time: sqs delivers it again each time its visibility timeout ends, until it is 14 days old. `kdst invoke --stage development --function worker --path .koldstart/events/worker/batch.json` hands the worker a batch by hand, and answers with the two messages it failed.

## related

- [dead-letter-queues](../dead-letter-queues): where a message goes after it has failed a few times
- [link](../link): the access and the variables of `jobs` and `processed`
- [alarms](../alarms): a queue that holds messages too long
