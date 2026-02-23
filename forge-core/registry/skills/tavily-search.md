---
name: tavily-search
description: Search the web using Tavily AI search API
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
---

# Tavily Web Search Skill

Search the web using the Tavily AI search API, optimized for LLM applications.

## Authentication

Set the `TAVILY_API_KEY` environment variable with your Tavily API key.
Get your key at https://tavily.com

No OAuth or MCP configuration required.

## Quick Start

```bash
./scripts/tavily-search.sh '{"query": "latest AI news"}'
```

## Tool: tavily_search

Search the web using Tavily AI.

**Input:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| query | string | yes | The search query |
| search_depth | string | no | `basic` (fast) or `advanced` (thorough). Default: `basic` |
| max_results | integer | no | Maximum results to return (1-20). Default: 5 |
| time_range | string | no | Filter by time: `day`, `week`, `month`, `year` |
| include_domains | array | no | Only include results from these domains |
| exclude_domains | array | no | Exclude results from these domains |

**Output:** JSON object with `query`, `answer`, `results` (array of `{title, url, content, score}`), and `response_time`.

### Search Depth

| Depth | Speed | Detail | Use Case |
|-------|-------|--------|----------|
| basic | Fast (~1s) | Standard snippets | Quick lookups, fact checks |
| advanced | Slower (~3s) | Detailed content | Research, analysis |

### Response Format

```json
{
  "query": "your search query",
  "answer": "AI-generated summary answer",
  "response_time": 0.5,
  "results": [
    {
      "title": "Page Title",
      "url": "https://example.com",
      "content": "Relevant content snippet...",
      "score": 0.95
    }
  ]
}
```

### Tips

- Use `search_depth: advanced` for research tasks that need detailed content
- Use `include_domains` to restrict searches to trusted sources
- Use `time_range: day` for breaking news or very recent information
- The `answer` field provides a concise AI-generated summary when available
