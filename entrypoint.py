import os
import sys
import duckdb

def main():
    query = os.environ.get('QUERY')
    if not query:
        print("Error: QUERY environment variable is required.")
        sys.exit(1)

    input_path = os.environ.get('INPUT_PATH')
    output_path = os.environ.get('OUTPUT_PATH')

    # Connect to an in-memory database
    con = duckdb.connect()

    # Load pre-installed extensions and set up AWS credentials
    con.execute("""
        LOAD spatial;
        LOAD vss;
        LOAD httpfs;
        LOAD aws;
        CALL load_aws_credentials();
    """)

    # Optional: define a session variable for INPUT_PATH if provided,
    # so the user can easily reference it via getvariable('input_path')
    if input_path:
        safe_input = input_path.replace("'", "''")
        con.execute(f"SET variable input_path = '{safe_input}';")

    try:
        if output_path:
            print(f"Executing query and writing to {output_path}...")
            safe_output = output_path.replace("'", "''")
            
            # Wrap the query in a COPY statement to output the results
            # DuckDB will infer the format based on the extension of OUTPUT_PATH
            final_query = f"COPY ({query}) TO '{safe_output}';"
            con.execute(final_query)
            print("Done.")
        else:
            print("Executing query...")
            res = con.execute(query)
            
            # Display results if the query returned any
            if res:
                # show() nicely prints a formatted table to stdout
                res.show()
    except Exception as e:
        print(f"Error executing query:\n{e}")
        sys.exit(1)

if __name__ == "__main__":
    main()
