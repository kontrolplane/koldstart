//! api answers GET /hello on the stage's http api.

use lambda_runtime::{Error, LambdaEvent, service_fn};
use serde_json::{Value, json};

async fn handle(event: LambdaEvent<Value>) -> Result<Value, Error> {
    let name = event.payload["queryStringParameters"]["name"]
        .as_str()
        .unwrap_or("world");
    let body = json!({ "message": format!("hello {name}") });
    Ok(json!({
        "statusCode": 200,
        "headers": { "content-type": "application/json" },
        "body": body.to_string(),
    }))
}

#[tokio::main]
async fn main() -> Result<(), Error> {
    lambda_runtime::run(service_fn(handle)).await
}
