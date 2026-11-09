# resources

`resources` holds anything else aws offers, by the CloudFormation type name and properties, made through the cloud control api: tables, queues, buckets and topics, and policies, subscriptions, alarms, keys or any other type. A resource is named in the configuration, `["documents"]`, and that name is how functions [link](../link) it, events read it and other resources refer to it.

```pkl
["documents"] = (kdst.bucket()) {
  properties {
    BucketName = named("documents")
    VersioningConfiguration { Status = "Enabled" }
  }
}

["documents-tls-only"] {
  type = "AWS::S3::BucketPolicy"
  properties {
    Bucket = kdst.ref("documents", "BucketName")
    ...
  }
}
```

The shorthands give a resource with its type and the properties most want, which what is written after amends:

| shorthand              | gives                                                                          |
| ---------------------- | ------------------------------------------------------------------------------ |
| `kdst.table("sku")`    | a dynamodb table keyed by `sku`, on-demand, with point-in-time recovery; `sortKey` and `sortKeyType` add a sort key |
| `kdst.queue()`         | an sqs queue that keeps a message for 14 days rather than 4                    |
| `kdst.bucket()`        | an s3 bucket with public access blocked and its objects encrypted              |
| `kdst.topic()`         | an sns topic                                                                   |

`kdst.ref("documents", "Arn")` reads an attribute of another resource once it exists, as CloudFormation's `Fn::GetAtt` names them, so koldstart makes that one first; `kdst.join()` puts refs and strings together. A resource without a name gets one cloud control makes up; `named()`, a local function of [koldstart.pkl](./koldstart.pkl), gives `<app>-<stage>-<name>` instead.

A plan checks the properties against the type's schema in the cloudformation registry. Tables, buckets, keys, secrets, streams, databases and the other types that hold data are retained: `kdst remove` leaves them in place, and the next deploy of the stage takes them over again. `deletionPolicy = "delete"` deletes one with the stage, as `thumbnails` here; `"retain"` keeps one of any other type. A change that replaces a retained resource stops the plan unless `--allow-replace` is passed, and the old one stays.

## trying it

```bash
kdst plan --stage development
kdst deploy --stage development
aws s3 cp specification.pdf "s3://resources-development-documents/incoming/socks-blue/specification.pdf"
kdst logs --stage development --function catalogue
kdst remove --stage development
```

The plan lists each resource with its type, in the order the refs make: the bucket before its policy, the topic and queue before the subscription. A put under `incoming/` stores a version of `socks-blue` in `products` and publishes it to `changes`, which delivers it to `imports`. `remove` deletes the stage and `thumbnails`, and says it left `documents` and `products` in place.

A resource that exists already, made by hand or by another tool, is taken over with `kdst import`, by the identifier cloud control knows it by: `kdst import products resources-development-products --stage development`. It changes nothing in aws; the next deploy brings it in line with the configuration.

## related

- [link](../link): letting a function use a resource
- [permissions](../permissions): a statement on a resource link does not know
- [encryption-keys](../encryption-keys): a resource that encrypts others
