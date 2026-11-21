# Web answer provider contract audit

Reviewed 2026-10-31. Most of this audit checks the current Searchwire adapters
with synthetic HTTP fixtures. The OpenCode Go live probe is described below.
It does not establish that every provider supports every configured model, or
that an answer is factually accurate. A passing fixture checks only the
behavior named by the test, not provider maturity as a whole.

## Provenance

Public Postman collections were checked first. The [Perplexity collection](https://www.postman.com/ai-on-postman/perplexity/documentation/w7cqeup/perplexity)
contains saved Sonar `/chat/completions` responses, not the Agent `/v1/agent`
contract used here. The [Postman Claude collection](https://www.postman.com/postman/anthropic-apis/collection/dhus72s/claude-api)
and [OpenAI collection](https://www.postman.com/devrel/openai/documentation/k25n3c8/openai-api)
provide general API examples, but the inspected material did not supply the
web-search response and edge-case examples needed here. The inspected public
Postman search results did not yield suitable matching web-answer examples for
OpenRouter, OpenCode, xAI, Brave, or OmniRoute. Absence from these search
results is not proof that none exist.

Consequently, fixtures in `answer_contract_test.go` are **minimal synthetic
examples**, with fictitious text and URLs; they are not recorded Postman or
live responses. Their shapes are adapted from the following provider sources:

| Provider | Source used | What it establishes and what it does not |
| --- | --- | --- |
| OpenAI | [Web search output and citations](https://developers.openai.com/api/docs/guides/tools-web-search), [response status](https://developers.openai.com/api/reference/resources/responses/streaming-events) | Web-search items, text annotations and incomplete status. The incomplete fixture adds synthetic partial text to test handling, not to claim that every incomplete response includes it. |
| OpenRouter | [Server tools](https://openrouter.ai/docs/guides/features/server-tools/overview), [chat API reference](https://openrouter.ai/docs/api/reference/overview) | Server tool and chat response conventions. The minimal completion is synthetic; it does not prove a real search happened. |
| Perplexity | [Official Agent migration guide](https://github.com/perplexityai/api-platform-developers/blob/main/skills/migrate-sonar-to-agent-api/SKILL.md) | Agent output `search_results`, and HTTP 200 failed/cancelled states. The partial-text failure combination is synthetic. |
| Anthropic | [Web search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool), [server tools and pause_turn](https://platform.claude.com/docs/en/agents-and-tools/tool-use/server-tools) | Content blocks, citations, in-body search errors and continuation. The combined error-plus-text example is synthetic. |
| Brave | [Answers API reference](https://api-dashboard.search.brave.com/api-reference/ai/answers), [Answers guide](https://api-dashboard.search.brave.com/documentation/services/answers) | Answers request/response conventions and citation/streaming requirements. |
| xAI | [Web search](https://docs.x.ai/developers/tools/web-search), [citations](https://docs.x.ai/developers/tools/citations) | Responses request and inline citation conventions. |
| OmniRoute | [Upstream search response example](https://github.com/diegosouzapw/OmniRoute/issues/10628) | The `/v1/search` wrapper and results shape; the answer-plus-results fixture is synthetic. |
| OpenCode | [Go session requirements](https://opencode.ai/v2/docs/console/go/#where-can-i-use-it), [model endpoints](https://dev.opencode.ai/v2/docs/console/models/) | The synthetic fixture was subsequently corroborated by the bounded live probe below; the response content is not a recording. |

All provider normal-path fixtures exercise the adapter through `Searcher.AnswerWithOptions`
with a fake HTTP transport. Each provider is also tested against malformed JSON
and HTTP 429. These scenarios cannot establish correct handling of all errors.

## Results

The ordinary suite passes. It verifies basic text extraction for all eight
synthetic success shapes, Anthropic citation extraction, xAI/OpenRouter text
containing an inline URL, and malformed JSON / HTTP 429 handling for each
provider. These results do not establish actual grounding or live support.

The following eight regression cases previously failed and now pass:

| Provider | Case | Previous behavior in fake HTTP test | Current behavior |
| --- | --- | --- | --- |
| OpenAI | URL exists only in `output_text.annotations` | Answer returned without URL | Citation retained in answer text |
| OpenAI | `status: incomplete` plus partial text | Partial text returned as ordinary success | Incomplete status returns an error |
| OpenRouter | `finish_reason: length` plus partial text | Partial text returned as ordinary success | Truncation returns an error |
| Perplexity | URL in `search_results` output item | Answer marker returned without source URL | Search-result URLs retained in answer text |
| Perplexity | `status: failed` plus partial text | Partial text returned as ordinary success | Failure status returns an error |
| Anthropic | Search tool error plus text | Text returned without structured search error | Tool error returns an error |
| Anthropic | `pause_turn` | Returns `ErrInvalidProviderResponse` after one request | Paused content is replayed, with bounded continuation |
| OmniRoute | Answer and results both present | Answer returned; result URLs dropped | Result URLs retained in answer text |

The desired-contract assertions express the proposed behavior of surfacing
sources, failures and incomplete output. The current `Answer` API returns
text, provider, question and search status, so the source tests look for URLs
in text. If a structured source contract is introduced, update the assertions to
inspect that contract rather than requiring URLs in text. Treating all search
errors as fatal is also a policy choice: a future API could instead return
partial results with an explicit error/status.

Providers may decide not to search. `searchStatus` reports observed provider
activity, absence of an observed event, or unknown status; it does not certify
the factual correctness of an answer. Brave citation-enabled streaming is
consumed internally and returned as completed text with links. The default
answer HTTP timeout is now 60 seconds unless explicitly configured otherwise;
no latency distribution was measured.

### OpenCode Go live probe

On 2026-10-31, with the user's authorization and a configured Go API key, a
call through the unchanged `Searcher.AnswerWithOptions` adapter to
`https://opencode.ai/zen/go/v1/responses` with model `gpt-5.6-luna` returned
HTTP 400 `MissingSessionID` in about 0.7 seconds. The adapter did not send
`x-opencode-session`. The [OpenCode Go documentation](https://opencode.ai/v2/docs/console/go/#where-can-i-use-it)
instructs clients to send a stable session ID per conversation.

A second diagnostic request used the same model, endpoint, question and
`web_search` tool, but injected that header outside Searchwire. It returned
HTTP 200 and `status: completed` in 9.593 seconds, with completed `search`,
`open_page`, and `find_in_page` web-search actions plus an answer containing
five citation annotations. This confirms provider-hosted search for that
combination, not general support for every model or successful operation of
the original adapter. A subsequent request through the fixed adapter returned
HTTP 200, reported three completed web actions, and took 7.646 seconds.
A further call through the fixed adapter with the default internal HTTP client
returned `searchStatus=observed` in 6.857 seconds. None of these timings
establishes a latency distribution. The reconstructed
fixtures use placeholders and do not store the API key or the full response.

## Run

```sh
go test ./...
go test -run '^TestAnswerDesiredContract$' -v
```

The second command isolates the regression cases; it does not call providers.
