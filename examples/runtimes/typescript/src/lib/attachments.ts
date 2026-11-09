/** The types of file a client may attach. */
export const contentTypes = ["application/pdf", "image/jpeg", "image/png", "text/plain"] as const;
export type ContentType = (typeof contentTypes)[number];

/** What a client asks to upload: the file's name, as it is offered back on download, and its type. */
export interface Requested {
  fileName: string;
  contentType: ContentType;
}

export type Parsed<T> = { ok: true; value: T } | { ok: false; error: string };

/** The id an attachment gets: a uuid, as randomUUID makes one. */
export const identifier = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/** Names a file can have on any machine it is downloaded to: no paths, quotes or control characters. */
const fileName = /^[^/\\"\u0000-\u001f\u007f]{1,255}$/;

/** Reads what a client asks to upload from json, or says what is wrong with it. */
export const parse = (body: string): Parsed<Requested> => {
  let value: unknown;
  try {
    value = JSON.parse(body);
  } catch {
    return { ok: false, error: "the body is not json" };
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return { ok: false, error: "the body is not an object" };
  }
  const { fileName: name, contentType } = value as Record<string, unknown>;
  if (typeof name !== "string" || !fileName.test(name) || name === "." || name === "..") {
    return { ok: false, error: "fileName is not 1 to 255 characters without /, \\, quotes or control characters" };
  }
  if (typeof contentType !== "string" || !contentTypes.includes(contentType as ContentType)) {
    return { ok: false, error: `contentType is not one of ${contentTypes.join(", ")}` };
  }
  return { ok: true, value: { fileName: name, contentType: contentType as ContentType } };
};

/** Where an attachment's file is kept in the bucket: under its id, by its name. */
export const objectKey = (id: string, name: string): string => `attachments/${id}/${name}`;

/** What every key of an attachment starts with, to find its file by its id. */
export const objectPrefix = (id: string): string => `attachments/${id}/`;
