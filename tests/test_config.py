import pytest

from pqx import config


def test_save_load_merge(config_home):
    p = config.formats_path()
    assert p == config_home / "pqx" / "formats.yaml"
    assert config.load_formats() == {}
    config.save_format("ra", ".4f")
    config.save_format("psfFlux", 3)
    config.save_format("odd", ".5")  # would read back as a float if not quoted
    text = p.read_text()
    assert text.startswith("# pqx column display formats")
    assert config.load_formats() == {"ra": ".4f", "psfFlux": 3, "odd": ".5"}
    # another session's change is kept: save re-reads the file before writing
    p.write_text(text + "  dec: .2f\n")
    config.save_format("ra", None)
    assert config.load_formats() == {"psfFlux": 3, "odd": ".5", "dec": ".2f"}
    assert not list(p.parent.glob("*.tmp"))  # no temp files left behind


def test_hand_edited_and_corrupt(config_home):
    p = config.formats_path()
    p.parent.mkdir(parents=True)
    p.write_text("columns:\n  ra: .3f\n  bad: [1, 2]\n  neg: -1\n  empty: ''\n")
    assert config.load_formats() == {"ra": ".3f"}
    p.write_text("columns: [ra\n")
    with pytest.raises(config.ConfigError):
        config.load_formats()
    with pytest.raises(config.ConfigError):  # never overwrite a file we can't parse
        config.save_format("ra", 2)
    assert p.read_text() == "columns: [ra\n"


def test_symlink_mode_and_bad_values(config_home, tmp_path):
    real = tmp_path / "dotfiles" / "formats.yaml"
    real.parent.mkdir()
    real.write_text("columns:\n  ra: .3f\n  huge: 100000000\n  slow: .100000000f\n")
    real.chmod(0o644)
    p = config.formats_path()
    p.parent.mkdir(parents=True)
    p.symlink_to(real)
    assert config.load_formats() == {"ra": ".3f"}  # out-of-range entries are dropped
    config.save_format("dec", 2)
    assert p.is_symlink() and (real.stat().st_mode & 0o777) == 0o644
    assert config.load_formats() == {"ra": ".3f", "dec": 2}


def test_binary_file_is_a_config_error(config_home):
    p = config.formats_path()
    p.parent.mkdir(parents=True)
    p.write_bytes(b"\xff\xfe\x00columns")
    with pytest.raises(config.ConfigError):
        config.load_formats()


def test_parse_override():
    assert config.parse_override(" 3 ") == 3
    assert config.parse_override("²") == "²"  # isdigit() but not an int
    assert config.parse_override(".2e") == ".2e"
    assert config.parse_override("") is None


def test_cli_format_option(demo_path, monkeypatch, capsys):
    from pqx import cli
    import pqx.app

    seen = {}

    class FakeApp:
        def __init__(self, path, **kw):
            seen.update(kw["formats"])

        def run(self):
            pass

    monkeypatch.setattr(pqx.app, "PqxApp", FakeApp)
    assert cli.main([demo_path, "--format", "ra =.2f", "--format", "mag=2", "--format", "t=%Y"]) == 0
    assert seen == {"ra": ".2f", "mag": 2, "t": "%Y"}
    for bad in ("ra", "ra=", "ra=q", "ra=²x", "ra=99"):
        with pytest.raises(SystemExit):
            cli.main([demo_path, "--format", bad])
    assert not config.formats_path().exists()  # --format is never saved
