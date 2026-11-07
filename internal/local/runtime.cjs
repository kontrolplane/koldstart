// The runtime client koldstart runs node handlers with locally: it asks the runtime api for the
// next event, calls the handler, async or with a callback, and posts what it returns or throws.
'use strict'

const http = require('node:http')

const api = process.env.AWS_LAMBDA_RUNTIME_API
const [file, exportName] = process.argv.slice(2)
const base = '/2018-06-01/runtime'

function request (method, path, body, headers) {
  return new Promise((resolve, reject) => {
    const [host, port] = api.split(':')
    const req = http.request({ host, port, method, path: base + path, headers }, res => {
      const chunks = []
      res.on('data', c => chunks.push(c))
      res.on('end', () => resolve({ headers: res.headers, body: Buffer.concat(chunks).toString('utf8') }))
    })
    req.on('error', reject)
    if (body !== undefined) req.write(body)
    req.end()
  })
}

function serialiseError (err) {
  const e = err instanceof Error ? err : new Error(String(err))
  return JSON.stringify({
    errorType: e.name || 'Error',
    errorMessage: e.message,
    stackTrace: (e.stack || '').split('\n').slice(1).map(l => l.trim())
  })
}

function call (handler, event, context) {
  if (handler.length >= 3) {
    return new Promise((resolve, reject) => {
      const done = (err, value) => (err ? reject(err) : resolve(value))
      const returned = handler(event, context, done)
      if (returned && typeof returned.then === 'function') returned.then(resolve, reject)
    })
  }
  return Promise.resolve().then(() => handler(event, context))
}

async function main () {
  let handler
  try {
    const mod = require(file)
    handler = mod[exportName]
    if (typeof handler !== 'function') {
      throw Object.assign(new Error(`${file} exports no function ${exportName}`), { name: 'Runtime.HandlerNotFound' })
    }
  } catch (err) {
    await request('POST', '/init/error', serialiseError(err), { 'Lambda-Runtime-Function-Error-Type': 'Runtime.ImportModuleError' })
    process.exit(1)
  }

  for (;;) {
    const next = await request('GET', '/invocation/next')
    const id = next.headers['lambda-runtime-aws-request-id']
    const deadline = Number(next.headers['lambda-runtime-deadline-ms'])
    process.env._X_AMZN_TRACE_ID = next.headers['lambda-runtime-trace-id'] || ''
    const context = {
      awsRequestId: id,
      functionName: process.env.AWS_LAMBDA_FUNCTION_NAME,
      functionVersion: process.env.AWS_LAMBDA_FUNCTION_VERSION,
      invokedFunctionArn: next.headers['lambda-runtime-invoked-function-arn'],
      memoryLimitInMB: process.env.AWS_LAMBDA_FUNCTION_MEMORY_SIZE,
      logGroupName: process.env.AWS_LAMBDA_LOG_GROUP_NAME,
      logStreamName: process.env.AWS_LAMBDA_LOG_STREAM_NAME,
      callbackWaitsForEmptyEventLoop: true,
      getRemainingTimeInMillis: () => Math.max(0, deadline - Date.now())
    }
    try {
      const result = await call(handler, JSON.parse(next.body), context)
      await request('POST', `/invocation/${id}/response`, JSON.stringify(result === undefined ? null : result))
    } catch (err) {
      await request('POST', `/invocation/${id}/error`, serialiseError(err), { 'Lambda-Runtime-Function-Error-Type': 'Unhandled' })
    }
  }
}

main()
