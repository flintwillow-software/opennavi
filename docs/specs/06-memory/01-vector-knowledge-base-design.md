# Vector Knowledge Base — Design Reference

> Design notes for adding vector-based knowledge base support to the memory system, extracted from the original gap analysis. sqlite-vec is the recommended starting point for local-first vector search.

## Recommendation: sqlite-vec

Start with **sqlite-vec** — a pure C SQLite extension that adds vector search as a virtual table. No external service, no network dependency. The navi already ships with SQLite for session storage.

```sql
-- Create a vector index for knowledge base entries
CREATE VIRTUAL TABLE kb_embeddings USING vec0(
    embedding float[768]
);

-- Query: find similar resolved tickets
SELECT rowid, distance
FROM kb_embeddings
WHERE embedding MATCH ?
  AND k = 5;
```

## RAG Library Landscape (Go-native or embeddable)

| Library | Type | Backend | Notes |
|---------|------|---------|-------|
| **[raggo](https://github.com/teilomillet/raggo)** | Go lib | OpenAI, Milvus, chromem | Simple RAG, Contextual RAG, Memory Context. Chunking, embeddings, hybrid search. |
| **[gorag](https://github.com/StacklokLabs/gorag)** | Go lib | Ollama, OpenAI, pgvector, Qdrant | Multi-vector DB support. Backend abstraction. |
| **[sqlite-vec](https://github.com/asg017/sqlite-vec)** | C ext | SQLite (embedded) | Pure C, no deps. Runs anywhere SQLite runs. **Best fit for local navis.** |
| **[chromem-go](https://github.com/philippgille/chromem-go)** | Go lib | In-memory + persist | Embedded vector DB for Go. No external server. |

## Design

- Knowledge base is a 5th memory tier (vector-based, beyond the 4 tiers in the memory spec)
- Populated from resolved tickets, documentation, runbooks, FAQs
- Queried via semantic search (not keyword matching)
- Supports auto-population (agent writes to KB after resolving an issue)
- Local-only (sqlite-vec) by default, external (Qdrant, pgvector) for larger deployments

## Related

- [Memory System Spec](../06-memory/README.md) — the formal spec this design detail extends