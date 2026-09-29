"""pqx — a fast, friendly terminal explorer for Parquet files."""

try:  # written by setuptools-scm from the git tags at build/install time
    from pqx._version import version as __version__
except ImportError:  # a source checkout that was never built or installed
    __version__ = "0.0.0.dev0"
