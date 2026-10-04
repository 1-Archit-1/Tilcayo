import os
import re
import sys

from shared.storage import open_connection, run_query

EXTENSIONS = [e.strip() for e in os.environ.get('DUCKDB_EXTENSIONS', '').split(',') if e.strip()]

# Lazy-load the embedding model only if the query needs it
_model = None


def get_model():
    global _model
    if _model is None:
        from sentence_transformers import SentenceTransformer
        model_name = os.environ.get('EMBEDDING_MODEL', 'all-MiniLM-L6-v2')
        _model = SentenceTransformer(model_name)
    return _model


def resolve_embeds(query: str) -> str:
    """
    Find all embed('...') calls in the query and replace them with
    literal DuckDB float array syntax, e.g. [0.1, 0.2, ...]::FLOAT[384]

    Supports single or double quoted strings inside embed().
    """
    pattern = re.compile(r"embed\(\s*['\"](.+?)['\"]\s*\)", re.IGNORECASE)
    matches = pattern.findall(query)

    if not matches:
        return query

    model = get_model()

    def replace_match(m):
        text = m.group(1)
        vector = model.encode(text).tolist()
        dim = len(vector)
        formatted = ', '.join(f'{v:.8f}' for v in vector)
        return f'[{formatted}]::FLOAT[{dim}]'

    return pattern.sub(replace_match, query)


def main():
    query = os.environ.get('QUERY')
    if not query:
        print("Error: QUERY environment variable is required.", file=sys.stderr)
        sys.exit(1)

    output_path = os.environ.get('OUTPUT_PATH')

    # Resolve embed() macros before DuckDB sees the query
    try:
        query = resolve_embeds(query)
    except Exception as e:
        print(f"Error generating embeddings:\n{e}", file=sys.stderr)
        sys.exit(1)

    # Single-SELECT guard runs inside run_query, i.e. after embed() resolution
    con = open_connection(EXTENSIONS, output_path)
    run_query(con, query, output_path)


if __name__ == "__main__":
    main()
