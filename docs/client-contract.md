# Client compatibility

The first release connects OpenCode and the OpenAI Python SDK to Codex through
`POST /v1/responses` (JSON or SSE) and `GET /v1/models`.

The gateway can [run locally](running.md). These are required checks, not completed
client compatibility guarantees. Test failures and cancellation locally; verify supported
generation scenarios with the real clients and Codex before release.

## Required acceptance matrix

| Scenario | Required behavior | Client check |
|---|---|---|
| Full history | Send complete prior input on each request with `store:false`; do not depend on stored responses or `previous_response_id`. | Python JSON; OpenCode agent turn |
| Text and instructions | Preserve text input and top-level instructions. JSON returns a complete response and output message. | Python JSON; OpenCode turn |
| Function loop and fragments | Preserve function name, `call_id`, JSON argument fragments and result output. The next request sends `function_call_output` with the same `call_id` and full history. | Python loop; OpenCode tool turn |
| Screenshots and images | Accept screenshot/image input as the supported URL or data URL form and preserve it as `input_image`. File IDs and Files resources are not implied. | OpenCode screenshot turn |
| JSON and JSON Schema | Preserve `text.format` for JSON and JSON Schema output and return the complete structured response. Verify exact Codex support, including any `json_object` variant, live. | Python JSON/schema |
| Opaque reasoning | Preserve reasoning item IDs, summaries, encrypted content and ordering through a tool loop without interpreting the content. | Python loop; OpenCode tool turn |
| Incomplete, failure and cancel | Preserve terminal status and known versus unknown usage. EOF before a terminal event is an error; cancellation stops the attempt and releases its resources. | Python and OpenCode SSE |
| Model listing | `GET /v1/models` combines the Codex catalogs of enabled accounts. Temporary cooldowns do not remove models. Configure and test the selected models in OpenCode. | Python list; OpenCode config |

JSON requests must return a complete terminal response. SSE must preserve event
order and expose the terminal completed, incomplete or failed state; deltas alone
do not prove completion.

JSON and SSE use the same [final response assembly](decisions/0003-separate-request-execution-from-protocols.md#final-response-assembly).

## HTTP behavior

Both client routes require `Authorization: Bearer <client_key>`. Authentication
precedes body parsing and is checked again at admission. Listing models does not
occupy a generation slot. Handler responses have a server-generated `X-Request-ID`;
HTTP server rejections before routing do not.

POST requires uncompressed `application/json`; a charset parameter is allowed.
Bodies are limited to 16 MiB. Query parameters are unsupported. Unknown routes
return 404; unsupported methods return 405 with `Allow`.

| Limit | Value |
|---|---|
| Request headers | `net/http.MaxHeaderBytes` set to 16 KiB; five seconds to read |
| Generation request body | 60 seconds to read |
| JSON delivery | 30 seconds to write and flush |
| SSE delivery | 30 seconds per event and flush, cleared while waiting for Codex |
| Idle keep-alive connection | 60 seconds |

There is no total generation deadline. Upstream timing is defined in
[ADR 0003](decisions/0003-separate-request-execution-from-protocols.md#generation-timeouts).

Complete JSON uses HTTP 200 for `completed`, `incomplete` and `failed` responses.
Cancellation and cleanup failures take precedence over a terminal result.
Check `status` before using output. Without a valid terminal response, errors use
an OpenAI envelope: `{"error":{"message":"...","type":"...","code":"...","param":null}}`.

| Status | Meaning |
|---|---|
| 400 | Invalid request or unsupported model/option |
| 401 | Missing, invalid or revoked client key |
| 408 / 413 / 415 | Body timeout / body too large / unsupported content type or encoding |
| 429 | Key concurrency limit or provider quota |
| 500 | Internal failure |
| 502 | Upstream connection or protocol failure |
| 503 | No eligible account or service unavailable |
| 504 | Upstream timeout |

SSE starts only after the first valid event. It preserves event order and returns
each terminal event once. An interrupted upstream produces one safe `error` event
when the client connection remains writable; a failed client write ends delivery.
There is no `[DONE]` marker or synthetic completion. Local errors include a
`sequence_number` following the largest observed counter. Provider error events
with missing or invalid counters receive one; valid counters are preserved.

`GET /v1/models` returns an OpenAI list with `id`, `object: "model"`, `owned_by:
"openai"` and `created: 0` for each entry. Zero means the creation date is unknown.
The list does not describe model capabilities.

POST error responses set `X-Should-Retry: false`. The Python SDK respects this
header; use `max_retries=0` to disable its retries explicitly. OpenCode can retry
at its own layer, so CLAN cannot guarantee that clients never replay a request.

Generation requires a model ID in an enabled account's full catalog. Hidden
entries may be requested explicitly; unknown IDs are rejected without dispatch.
The [parameter contract](decisions/0002-use-responses-as-content-format.md#request-parameters)
rejects unknown or unsupported options with HTTP 400, except for the documented
`max_output_tokens` omission.

## Base request parameters

The parameter set in this section and the sections below is agreed. Reject
unlisted protocol options with HTTP 400; schema properties and tool data are not
protocol options.

For supported nullable options, `null` means omitted. Preserve explicit `false`
values and JSON Schema data. An unsupported option remains an error even when
its value is `null`.

| Parameter | Behavior |
|---|---|
| `model` | Required ID from an enabled account's full catalog. Never substitute another model. |
| `input` | String or array of supported Responses items. Convert a string to a message for Codex. |
| `instructions` | Optional string. Preserve its content. |
| `stream` | Boolean, default `false`. Return JSON when false and SSE when true. Codex always receives a streaming request. |
| `store` | Omitted or `false`. Reject `true` with HTTP 400. |
| `background` | Omitted or `false`. Reject `true` with HTTP 400. |
| `max_output_tokens` | Require a positive integer when non-null, then remove it and warn as described in the compatibility exception. The cap is not enforced. |
| `prompt_cache_key` | Optional string. Pass it to Codex; CLAN does not maintain a prompt cache. |

## Input and images

Accept messages with `user`, `assistant`, `developer` or `system` roles; convert
`system` to `developer` for Codex. Messages can contain text and images, including
prior output text and refusals. History can also contain reasoning items,
function calls and their results.

Images use HTTP(S) URLs without embedded credentials or base64 `data:image/` URLs.
Accept `detail` values `auto`, `low` and `high`; accept `original` only when the
model advertises support. The selected model must support image input. File IDs
are unsupported.

## Function tools

Accept `tools` entries with `type: "function"`, name, description, JSON Schema
parameters and `strict`. Accept `parallel_tool_calls` as a boolean and preserve
an explicit false value. Clients execute functions; CLAN carries definitions,
calls and results with their original `call_id`. Reject provider-native tools
in the first release.

The target `tool_choice` set is `auto`, `none`, `required`, or
`{"type":"function","name":"..."}`. Verify every mode with Codex before claiming
support: official Codex source confirms use of `auto`, while the client SDKs
can serialize the other forms. If a mode fails provider validation, revisit its
scope; never silently replace it with `auto`.

## Reasoning

Accept `reasoning.effort` values advertised for the model in the Codex catalog.
Reject unsupported values with HTTP 400; never substitute another effort level.
Accept `reasoning.summary` as `auto`, `concise` or `detailed` when the model
supports that parameter. Other reasoning controls, including `context` and `mode`,
are outside the first release.

For `include`, accept only `reasoning.encrypted_content`; an empty array is also
valid. Preserve returned reasoning IDs, summaries, encrypted content and item
order when clients replay history. Do not interpret the encrypted content.

## Text output

Accept `text.verbosity` as `low`, `medium` or `high` when the model supports it.
It may appear without `text.format`.

The target formats are `text` (also the default when format is omitted),
`json_object`, and `json_schema`. For `json_schema`, preserve `name`, `schema`,
and optional `description` and `strict`. Validate the parameter shape; leave
schema enforcement to Codex. Do not rewrite schemas or repair model output.
Preserve refusal and incomplete-response states.

Official Codex source uses verbosity and JSON Schema. Verify the explicit `text`
and `json_object` formats with Codex before claiming support. A client SDK's
ability to serialize a format does not prove provider support.

## Unsupported parameters

Reject `service_tier`, `temperature`, `top_p`, `top_logprobs`, `metadata`, `user`,
`safety_identifier`, `stream_options`, and cache-retention controls with HTTP 400.
Do not discard them silently. Stored-response references (`previous_response_id`
and `conversation`) are also unsupported; clients send full history.

## Checks so far

Source review used [OpenCode v1.18.30](https://github.com/anomalyco/opencode/tree/3104c1428ec91f809e5ab86631300de41eb6952e)
with `@ai-sdk/openai` **3.0.88**, [OpenAI Python SDK v3.13.0](https://github.com/openai/openai-python/tree/f0fa922ef12f2c7329bcd8fc42e0cbb46f008ecb),
and [Codex at b4c864dd](https://github.com/openai/codex/tree/b4c864dd6497ae764e6a826300b34f7ca77ba965).

- **Local client checks:** eleven fake HTTP requests covered Python JSON/schema,
  history/function replay, image serialization, minimal SSE completion,
  EOF/incomplete behavior and `models.list`. These did not call Codex.
- **Live OAuth check, 2026-09-13:** browser sign-in, encrypted SQLite persistence,
  model discovery and token refresh passed through the production modules.
  The catalog returned seven models using `client_version=0.154.0`. Refresh
  preserved the ChatGPT account ID, and generation with renewed credentials passed.
- **Live generation on `gpt-5.6-luna`:** complete JSON, SSE, full-history replay,
  a function loop with five argument fragments, JSON Schema and `json_object`
  passed. Tool choices checked were a named function and `none`. Cancellation
  after the first text delta closed the local stream and preserved unknown usage.
  Codex omitted `Content-Type`; the adapter now accepts a missing header while
  still requiring valid SSE and a terminal event.
- **OAuth module tests:** local provider responses and temporary SQLite cover
  callback validation, token rotation, failed persistence and concurrent cancellation.
- **Gateway checks:** local HTTP providers and SQLite cover login, key creation,
  JSON/SSE, restart, revocation and shutdown. The built binary also passed startup,
  authenticated OpenAPI access and SIGTERM shutdown. These checks did not call Codex.
- **Pending:** live images, opaque reasoning replay, remaining tool-choice and
  format variants and remote/Docker callback forwarding.
  These module checks do not establish OpenCode or Python SDK compatibility.
  The full client matrix belongs to
  [#36](https://github.com/deyna256/clan/issues/36), after OAuth and HTTP integration.

## OpenCode

Use a custom provider ID `clan` with `@ai-sdk/openai` and a CLAN `baseURL` ending in
`/v1`. Do not use the built-in `openai` provider for this route: an existing
OpenAI OAuth profile can replace an explicit CLAN URL and key and [bypass the
gateway](https://github.com/anomalyco/opencode/blob/3104c1428ec91f809e5ab86631300de41eb6952e/packages/opencode/src/plugin/openai/codex.ts#L330-L438).

Configure model IDs and capabilities explicitly. OpenCode does not automatically
load a custom provider's models from `/v1/models`; see its
[provider loading code](https://github.com/anomalyco/opencode/blob/3104c1428ec91f809e5ab86631300de41eb6952e/packages/opencode/src/provider/provider.ts).

Start with this project-local `opencode.json`.
Set `CLAN_BASE_URL` to its URL ending in `/v1` and `CLAN_API_KEY` to a client key.
Replace `MODEL_ID` in all three places with a model from `/v1/models` and set its
capabilities to match. This configuration has been source-reviewed, not tested
against a running CLAN installation.

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "clan/MODEL_ID",
  "small_model": "clan/MODEL_ID",
  "provider": {
    "clan": {
      "npm": "@ai-sdk/openai",
      "name": "CLAN",
      "options": {
        "baseURL": "{env:CLAN_BASE_URL}",
        "apiKey": "{env:CLAN_API_KEY}"
      },
      "models": {
        "MODEL_ID": {
          "reasoning": true,
          "tool_call": true,
          "attachment": true,
          "temperature": false,
          "modalities": {"input": ["text", "image"], "output": ["text"]}
        }
      }
    }
  }
}
```

CLAN removes the `max_output_tokens` field that OpenCode supplies automatically.
The requested output limit is not enforced. This documented, logged
[compatibility exception](decisions/0002-use-responses-as-content-format.md#codex-compatibility-exception)
avoids a client plugin; it does not extend to other parameters.

## Python example

Set `CLAN_BASE_URL` to the gateway URL ending in `/v1`, `CLAN_API_KEY` to a CLAN
access key, and `CLAN_MODEL` to an ID returned by `/v1/models`.

```python
import os

from openai import OpenAI

client = OpenAI(
    base_url=os.environ["CLAN_BASE_URL"],
    api_key=os.environ["CLAN_API_KEY"],
    max_retries=0,
)
model = os.environ["CLAN_MODEL"]  # choose it from GET /v1/models
history = [{"role": "user", "content": "Hello"}]
reply = client.responses.create(
    model=model,
    input=history,
    instructions="Be concise.",
    store=False,
    include=["reasoning.encrypted_content"],
)
if reply.status != "completed":
    raise RuntimeError(f"Generation ended with status {reply.status}")
history.extend(item.model_dump(exclude_none=True) for item in reply.output)
print(reply.output_text)
# Append function_call_output with the original call_id, then send full history.
```

`max_retries=0` disables SDK
retries so client tests can observe CLAN's retry behavior directly.
