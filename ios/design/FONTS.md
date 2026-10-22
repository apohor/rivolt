# Bundled fonts (ios/Rivolt/Resources/Fonts)

Geist and Geist Mono, SIL Open Font License 1.1 (OFL.txt, shipped in the app bundle). No Reserved Font Name.
Only the static weights the design tokens use. Source: https://github.com/vercel/geist-font/releases/tag/v1.8.1
(published 2026-10-16), archive `geist-font-v1.8.1.zip`, sha256
`70f42c1452fb6df12ea183e20a65999157354f91d11dac18b4c713e383808ec2`.

| File | sha256 |
|---|---|
| OFL.txt | c683bfbcc7e087f5d37a54ef628f10387c451a83ddc459b151403a164ac46c90 |
| Geist-Regular.ttf | 4467c854246d29cf2fe8a18694f4dcd8996439e535d53b260b3bfb34c110175f |
| Geist-Medium.ttf | cf90d22c5f12977b06173440f6f564b7e1245aeb48bc4d5fbd82e6cde16abff5 |
| Geist-SemiBold.ttf | 9192802fb4e3cb725833b4a74b35ec6a03c62c44cf55b64864abd8de64bf720e |
| GeistMono-Medium.ttf | 6804a64ebc0cd275180959cfb334bd5c886a6a28fa2cc6508d80d17dc5c972e6 |
| GeistMono-SemiBold.ttf | b5c08511cdd2fc2a3320afa93cde3f21d57aed32475dd6ffd53dc4f9c061080d |

The web ships the same family as woff2 (web/src/assets/fonts, copied 2026-10-16). Fonts are registered at launch by
`Theme.registerFonts()`; a missing font falls back to the system font.
