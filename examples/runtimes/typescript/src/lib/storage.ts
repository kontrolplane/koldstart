import { GetObjectCommand, ListObjectsV2Command, PutObjectCommand, S3Client } from "@aws-sdk/client-s3";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";

/** The bucket, as the function uses it: urls to upload and download with, and the files kept. */
export interface Storage {
  /** A url a client puts the file to, with the content type it was asked for. */
  uploadUrl(key: string, contentType: string, lifetimeSeconds: number): Promise<string>;
  /** A url a client downloads the file from, offered under its name. */
  downloadUrl(key: string, fileName: string, lifetimeSeconds: number): Promise<string>;
  /** The key of the first file under prefix, or undefined when there is none yet. */
  find(prefix: string): Promise<string | undefined>;
}

/** The bucket link names as FILES_NAME, or an error that says what to do. */
const bucket = (): string => {
  const name = process.env.FILES_NAME;
  if (!name) {
    throw new Error("FILES_NAME is not set: link the files bucket in koldstart.pkl");
  }
  return name;
};

/** The client, made once for each instance of the function and kept between requests. */
const client = new S3Client({});

/**
 * The bucket in s3. A presigned url is signed with the function's credentials, so it lets the
 * client do what the function's role may, on that one key, until it expires.
 */
export const s3Storage = (): Storage => ({
  uploadUrl: (key, contentType, lifetimeSeconds) =>
    getSignedUrl(client, new PutObjectCommand({ Bucket: bucket(), Key: key, ContentType: contentType }), {
      expiresIn: lifetimeSeconds,
      signableHeaders: new Set(["content-type"]),
    }),
  downloadUrl: (key, fileName, lifetimeSeconds) =>
    getSignedUrl(
      client,
      new GetObjectCommand({
        Bucket: bucket(),
        Key: key,
        ResponseContentDisposition: `attachment; filename="${fileName}"`,
      }),
      { expiresIn: lifetimeSeconds },
    ),
  async find(prefix) {
    const output = await client.send(new ListObjectsV2Command({ Bucket: bucket(), Prefix: prefix, MaxKeys: 1 }));
    return output.Contents?.[0]?.Key;
  },
});
