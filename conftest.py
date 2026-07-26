import os
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent / "orchestrator"))

# investigate_plan_graph.py / implement_review_graph.py read MASUDA_STATE_DIR
# as a module-level constant at import time. Collection imports these modules
# before any test fixture runs, so a placeholder must already be set here --
# each test then overrides it per-test and reload()s the module (see
# orchestrator/tests/test_*.py's autouse fixtures) for real isolation.
os.environ.setdefault("MASUDA_STATE_DIR", tempfile.mkdtemp(prefix="masuda-state-placeholder-"))
