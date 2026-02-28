---
name: tavily-research
description: Deep multi-source research using Tavily Research API
metadata:
  forge:
    requires:
      bins:
        - curl
        - jq
      env:
        required:
          - TAVILY_API_KEY
        one_of: []
        optional: []
    egress_domains:
      - api.tavily.com
    timeout_hint: 300
---

# Tavily Research Skill

Perform deep, multi-source research using the Tavily Research API. Unlike basic search, research produces comprehensive reports (1000-3000 words) synthesizing information from multiple sources. Research tasks typically take 30-300 seconds depending on complexity and model.

## Authentication

Set the `TAVILY_API_KEY` environment variable with your Tavily API key.
Get your key at https://tavily.com

No OAuth or MCP configuration required.

## Quick Start

```bash
# Submit research request
./scripts/tavily-research.sh '{"input": "impact of quantum computing on cryptography"}'
# Returns: {"status": "pending", "request_id": "..."}

# Poll for results
./scripts/tavily-research-poll.sh '{"request_id": "72d4a81c-..."}'
# Returns: {"status": "completed", "summary": "...", "report": "...", ...}
```

## Workflow

The research API is asynchronous. Use the two tools in sequence:

1. Call `tavily_research` with your query — returns immediately with a `request_id`
2. Inform the user that research is in progress and may take 30-300 seconds
3. Call `tavily_research_poll` with the `request_id` — this tool waits internally until the research completes (up to ~5 minutes), so you only need to call it once
4. When the poll returns, include the full `report` text in your response — do not summarize or truncate it. Responses over 8000 characters are automatically delivered as a downloadable document by channel adapters (Telegram, Slack), giving the user the complete report as a file

## Tool: tavily_research

Submit a deep research request to Tavily AI. Returns immediately with a request_id for polling.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| input | string | yes | The research query or topic |
| model | string | no | Research model: `mini` (faster, ~30s), `pro` (thorough, ~300s), or `auto` (default). Default: `auto` |

**Output:** JSON object with `status` ("pending"), `request_id`, `input`, `model`, and `created_at`.

## Tool: tavily_research_poll

Wait for a previously submitted research request to complete and return the results. This tool handles polling internally — it waits up to ~5 minutes, retrying every 10 seconds until the research is done. You only need to call it once.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| request_id | string | yes | The request_id returned by tavily_research |

**Output:** JSON object with `status` ("completed"), `summary`, `topic`, `report`, `sources`, and `research_time`. Returns an error if the research fails or times out.

### Research Models

| Model | Speed | Depth | Use Case |
|-------|-------|-------|----------|
| mini | ~30s | Standard synthesis | Quick overviews, simple topics |
| pro | ~300s | Deep multi-source | Comprehensive analysis, complex topics |
| auto | Varies | Adaptive | Let the API choose based on query complexity |

### Response Format (completed)

```json
{
  "status": "completed",
  "summary": "Brief summary of key findings",
  "topic": "your research topic",
  "report": "Full multi-source research report (1000-3000 words)...",
  "sources": [
    {
      "title": "Source Title",
      "url": "https://example.com",
      "content": "Relevant excerpt..."
    }
  ],
  "research_time": 45.2
}
```

### Tips

- Use `model: pro` for topics requiring deep analysis across many sources
- Use `model: mini` for quick overviews where speed matters more than depth
- Research queries work best as descriptive topics rather than simple questions
- Always tell the user research is in progress before polling — it can take minutes
- Include the full `report` field verbatim in your response — do not summarize it. The channel adapter will send a brief summary as a message and attach the full report as a downloadable markdown file
- Prefix the report with a 1-2 sentence summary so the user gets immediate context before opening the file
