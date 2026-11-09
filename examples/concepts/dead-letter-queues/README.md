# dead-letter-queues

A dead-letter queue keeps what failed every time it was tried, so it is neither lost nor tried for ever, and someone can read it, fix the cause and send it back. There are three kinds, one for each way a function is handed its events:

| function | handed its events                    | after                       | in pkl                                          | the message holds                         |
| -------- | ------------------------------------ | --------------------------- | ----------------------------------------------- | ----------------------------------------- |
| `worker` | by lambda reading a queue            | three receives              | the queue's `RedrivePolicy`                     | the message as it was                     |
| `nightly`| without waiting, by a schedule       | three tries, or an hour     | `async = new { onFailure = ... }`               | the event, the error and the context      |
| `notify` | without waiting, by sns              | three tries                 | `deadLetter = ...`                              | the event, with the error in its attributes |

```pkl
["jobs"] = (kdst.queue()) {
  properties {
    RedrivePolicy {
      deadLetterTargetArn = kdst.ref("jobs-dead-letters", "Arn")
      maxReceiveCount = 3
    }
  }
}

async = new {
  maximumRetryAttempts = 2
  maximumEventAge = 1.h
  onFailure = kdst.ref("nightly-failures", "Arn")
}

deadLetter = kdst.ref("notify-dead-letters", "Arn")
```

koldstart gives each function's role `sqs:SendMessage` on its `onFailure` and `deadLetter`. For a function invoked without waiting, prefer `async.onFailure`: it says why the event failed, and can be a queue, a topic, an event bus or another function. A queue's dead-letter queue is set on the queue, as sqs moves the message itself.

## trying it

```bash
kdst deploy --stage development
outputs() { kdst outputs --stage development --output json; }
aws sqs send-message --queue-url "$(outputs | jq --raw-output .jobsQueue)" --message-body '{"task": "fail"}'
aws lambda invoke --function-name dead-letter-queues-development-nightly --invocation-type Event \
  --cli-binary-format raw-in-base64-out --payload '{"fail": true}' /dev/null
aws sns publish --topic-arn "$(outputs | jq --raw-output .announcementsTopic)" --message fail
```

Within a few minutes, once the retries are spent, each failure is in its queue:

```bash
aws sqs receive-message --queue-url "$(outputs | jq --raw-output .nightlyFailures)" --attribute-names All --message-attribute-names All
```

Once what made them fail is fixed, `aws sqs start-message-move-task --source-arn "$(outputs | jq --raw-output .jobsDeadLetters)"` moves the jobs back to `jobs`. A failed run of `nightly` holds its event as `requestPayload`, to invoke the function with again.

## related

- [queues](../queues): reporting only the messages of a batch that failed
- [schedules](../schedules): what invokes `nightly`
- [alarms](../alarms): paging when a dead-letter queue holds a message
