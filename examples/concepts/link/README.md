# link

`link` lets a function use a resource of its project. Name the resource and the access, `read`, `write` or `readwrite`, and koldstart adds a statement to the function's role with the actions of that access on that resource alone, and gives the function the resource's name, arn or url as environment variables. Use it for every resource of the project a function talks to: it is least privilege without writing a policy.

```pkl
link {
  ["stock"] = "read"
  ["restock"] = "write"
  ["reports"] = "readwrite"
  ["low-stock"] = "write"
}
```

The variables are the resource's name in upper case, with `-` as `_`, and a suffix for each attribute:

| type     | variables                          | read                                             | write                                    |
| -------- | ---------------------------------- | ------------------------------------------------ | ---------------------------------------- |
| table    | `STOCK_NAME`, `STOCK_ARN`          | `GetItem`, `BatchGetItem`, `Query`, `Scan`, on the table and its indexes | `PutItem`, `UpdateItem`, `DeleteItem`, `BatchWriteItem` |
| queue    | `RESTOCK_URL`, `RESTOCK_ARN`       | `ReceiveMessage`, `DeleteMessage`, `ChangeMessageVisibility` | `SendMessage`                  |
| bucket   | `REPORTS_NAME`, `REPORTS_ARN`      | `GetObject`, `ListBucket`                        | `PutObject`, `DeleteObject`, `ListBucket` |
| topic    | `LOW_STOCK_ARN`                    | `GetTopicAttributes`, `ListSubscriptionsByTopic` | `Publish`                                |

link also knows kinesis streams, secrets manager secrets and ssm parameters. A resource of a type it does not know is refused, `link does not know AWS::... yet: give access with permissions instead`, rather than given `*`. `readwrite` is both lists.

## trying it

```bash
kdst deploy --stage development
kdst invoke --stage development --function inventory
```

The function answers with the eight variables, the names and arns of what was made for the stage. `aws iam get-role-policy` on its role shows one statement for each linked resource.

## related

- [permissions](../permissions): access link does not give
- [resources](../resources): the resources link names
- [secrets](../secrets): values a function reads while deploying or at runtime
