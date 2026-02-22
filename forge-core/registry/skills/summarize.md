---
name: summarize
description: Summarize text or URLs using LLM
metadata:
  forge:
    requires:
      bins:
        - summarize
      env:
        required: []
        one_of:
          - OPENAI_API_KEY
          - ANTHROPIC_API_KEY
          - XAI_API_KEY
          - GEMINI_API_KEY
        optional:
          - FIRECRAWL_API_KEY
          - APIFY_API_TOKEN
---
## Tool: summarize_text

Summarize a block of text into key points.

**Input:** text (string) - The text to summarize
**Output:** A concise summary of the input text

## Tool: summarize_url

Fetch and summarize the content of a URL.

**Input:** url (string) - The URL to fetch and summarize
**Output:** A concise summary of the page content
