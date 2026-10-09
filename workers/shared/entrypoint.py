import os

from storage import fail, open_connection, run_query

EXTENSIONS = [e.strip() for e in os.environ.get('DUCKDB_EXTENSIONS', '').split(',') if e.strip()]


def main():
    query = os.environ.get('QUERY')
    if not query:
        fail("Error: QUERY environment variable is required.")

    output_path = os.environ.get('OUTPUT_PATH')

    con = open_connection(EXTENSIONS, output_path)
    run_query(con, query, output_path)


if __name__ == "__main__":
    main()
