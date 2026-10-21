# LiteLLM price subset

`litellm.json` is a subset of the LiteLLM price file. `koodam usage` embeds it.

- Source: https://github.com/BerriAI/litellm/blob/191967c20781877d606885f03f50721ab825ba0a/model_prices_and_context_window.json
- Commit: `191967c20781877d606885f03f50721ab825ba0a` (2026-10-06)
- Licence: MIT. `LICENSE` is the licence file of LiteLLM at the same commit.

The subset keeps the chat models of the providers `anthropic` and `openai`, and only 4 prices for each model: input, output, cache read and cache write. The prices are in USD for each token.

Do not edit `litellm.json` or `LICENSE` by hand. To make them again, change `LITELLM_COMMIT` in the `Makefile`, update the commit in this file, and run:

```sh
make prices
```

The target downloads the file and the licence at the commit, and runs `hack/litellm-prices`.
