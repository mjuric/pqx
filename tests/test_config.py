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
    assert not list(p.parent.glob(".formats.*"))  # no temp files left behind


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


def test_parse_override():
    assert config.parse_override(" 3 ") == 3
    assert config.parse_override(".2e") == ".2e"
    assert config.parse_override("") is None
