import os
import sys
import duckdb

from storage import configure_storage

EXTENSIONS = [e.strip() for e in os.environ.get('DUCKDB_EXTENSIONS', '').split(',') if e.strip()]


def main():
    query = os.environ.get('QUERY')
    if not query:
        print("Error: QUERY environment variable is required.", file=sys.stderr)
        sys.exit(1)

    output_path = os.environ.get('OUTPUT_PATH')

    con = duckdb.connect()

    for ext in EXTENSIONS:
        con.execute(f"LOAD {ext};")

    configure_storage(con, EXTENSIONS)

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
