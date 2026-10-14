# mikan: copyright, additional terms and credits

mikan  
Copyright (C) 2026 Miroshka and Mikan  
<https://github.com/Miroshka000/mikan>

mikan is free software: you can redistribute it and/or modify it under the terms of the GNU General Public License version 3 as published by the Free Software Foundation ([LICENSE](LICENSE)), with the additional terms below. It is distributed WITHOUT ANY WARRANTY; see the GNU General Public License for details.

## Additional terms (GNU GPL v3, section 7)

These terms apply to mikan as a whole and to every copy and modified version of it:

1. **Attribution, section 7(b).** Copies and modified versions must keep the copyright notice above and a link to <https://github.com/Miroshka000/mikan>, in this file and wherever the program shows its authorship.
2. **No passing off, section 7(c).** A modified version must be clearly marked as changed and as different from the original, and must not be presented as mikan or as an official mikan release.
3. **Names and logos, section 7(e).** No rights are granted to the name "mikan" or to the mikan logo and mascot. A modified version must use another name and another logo. See [TRADEMARK.md](TRADEMARK.md).

## Third-party code

- **[mihomo](https://github.com/MetaCubeX/mihomo)**, GPL-3.0, by MetaCubeX and contributors: the proxy core mikan runs.
- **[3x-ui](https://github.com/MHSanaei/3x-ui)**, GPL-3.0, by MHSanaei and contributors: parts of mikan are adapted from it, among them the local Happ crypt links (`internal/panel/subs/happcrypt.go`, after MHSanaei/3x-ui#6494, with the key from [Omegaplexx/hpwnr](https://github.com/Omegaplexx/hpwnr)).
- **Fonts:** Unbounded, Onest and JetBrains Mono, SIL Open Font License 1.1.

The other libraries mikan builds with keep their own licenses, listed in `go.mod`, `web/package.json` and `installer/Cargo.toml`.
