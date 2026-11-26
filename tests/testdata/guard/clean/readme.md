# Example tool

Install the tool with `make install`. Run the tests before each commit.
To configure the tool, copy `config.example.yaml` to `config.yaml` and set the API URL.
Send a pull request to the main branch when the tests pass.
Do not commit the file `config.yaml`. Read the rules in CONTRIBUTING.md first.
If a test fails, run it again with `-v` and read the output.
