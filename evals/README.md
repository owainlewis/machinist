# Local evals

`test_agent.py` exercises [`agent.py`](../agent.py) with a fake Codex SDK and GitHub CLI. It
never starts a coding agent or contacts GitHub.

```sh
python3 -m unittest discover -s evals -p 'test_*.py'
```
