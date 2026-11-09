# encryption-keys

A kms key of your own, made as a resource of the stage, encrypts what aws would otherwise encrypt with a key it owns: here a table and the function's environment variables. Use one when a policy asks for keys you control, rotate and can revoke, with every use of them in cloudtrail.

```pkl
["key"] {
  type = "AWS::KMS::Key"
  properties {
    EnableKeyRotation = true
    KeyPolicy { ... Principal { AWS = kdst.join(List("arn:", kdst.partition, ":iam::", kdst.accountId, ":root")) } ... }
  }
}

["entries"] = (kdst.table("id")) {
  properties { SSESpecification { SSEEnabled = true; SSEType = "KMS"; KMSMasterKeyId = kdst.ref("key", "Arn") } }
}
```

The key policy lets the account in, so iam policies decide who may use the key, and an administrator cannot lock everyone out. The function's `permissions` give it what dynamodb does with the key on its behalf, on this key alone. `kmsKey` encrypts the function's environment variables with it too, and its role may decrypt with it. The alias, `alias/encryption-keys-<stage>`, is how people and policies find the key.

A key holds data, as what it encrypted cannot be read without it, so `kdst remove` leaves it in place, and the table with it. Each key costs about $1 a month, for each stage.

## trying it

```bash
kdst deploy --stage development
kdst invoke --stage development --function notes --path .koldstart/events/notes/note.json
aws dynamodb describe-table --table-name "$(aws dynamodb list-tables --output text --query 'TableNames[?contains(@, `entries`)] | [0]')" --query Table.SSEDescription
```

The function answers with the note it stored and read back. The table's `SSEDescription` names the key of `keyArn`, as `kdst outputs --stage development` prints it, and cloudtrail shows the function's role using it.

## related

- [resources](../resources): what else a stage can make
- [permissions](../permissions): statements like the one on the key
- [secrets](../secrets): parameters encrypted with a key of your own need `kms:Decrypt` on it
