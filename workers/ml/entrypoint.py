import os
import re
import sys
import duckdb

EXTENSIONS = [e.strip() for e in os.environ.get('DUCKDB_EXTENSIONS', '').split(',') if e.strip()]

def configure_storage(con: duckdb.DuckDBPyConnection):
    """
    Configure httpfs for S3-compatible storage providers.
    See workers/shared/entrypoint.py for full documentation.
    """
    if 'httpfs' not in EXTENSIONS:
        return

    endpoint  = os.environ.get('TILCAYO_S3_ENDPOINT')
    url_style = os.environ.get('TILCAYO_S3_URL_STYLE', 'vhost')
    use_ssl   = os.environ.get('TILCAYO_S3_USE_SSL', 'true').lower()

    if endpoint:
        con.execute(f"SET s3_endpoint='{endpoint}';")

    con.execute(f"SET s3_url_style='{url_style}';")

    if use_ssl in ('false', '0', 'no'):
        con.execute("SET s3_use_ssl=false;")

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

    con = duckdb.connect()

    for ext in EXTENSIONS:
        con.execute(f"LOAD {ext};")

    configure_storage(con)

    try:
        if output_path:
            con.execute(f"COPY ({query}) TO '{output_path}'")
        else:
            con.sql(query).show()
    except Exception as e:
        print(f"Error executing query:\n{e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
