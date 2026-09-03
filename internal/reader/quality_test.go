package reader

import "testing"

// Samples captured from the live tantra-expert corpus. The cipher sample comes
// from "I AM THAT Nisargadatta Maharaj_OCR.pdf", whose embedded font subsets
// carry no usable ToUnicode CMap; it decodes as a substitution cipher
// ("RraaHUs" is "freedom") and 1,422 such chunks reached the vector index as
// unretrievable noise before this check existed.
func TestAssessTextOnRealCorpusSamples(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		suspect bool
	}{
		{"english prose", sampleEnglish, false},
		{"iast transliteration", sampleIAST, false},
		{"dense index tables", sampleTables, false},
		{"glyph cipher", sampleCipher, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := AssessText(tt.text)
			if q.Suspect != tt.suspect {
				t.Errorf("Suspect = %v, want %v (midcap=%.3f meanTok=%.1f letter=%.2f reason=%q)",
					q.Suspect, tt.suspect, q.MidWordCapitalRatio, q.MeanTokenLength, q.LetterRatio, q.Reason)
			}
			if tt.suspect && q.Reason == "" {
				t.Error("Suspect text must carry a Reason")
			}
		})
	}
}

// TestAssessTextShortInputIsNotJudged guards the rune-based sample gate: the
// corruption destroys word boundaries, so a token-count gate would be defeated
// by the very text it must judge.
func TestAssessTextShortInputIsNotJudged(t *testing.T) {
	if q := AssessText("short bit of text"); q.Suspect {
		t.Errorf("short input should not be judged, got reason %q", q.Reason)
	}
}

const (
	sampleEnglish = "Qexperiencing them. Then, you will discover one of the greatest secrets\nof life – you can experience life in any way you choose. You do not have to react\nto the external world. To experience love, simply choose to be love. Even when\nyou are experiencing pain choose to be love. Then the pain will disappear and\nyou will experience love.\n\n□—142—□\n\n---\n\n## Vigyan Bhairava Tantra\n\n### THE MEDITATIONS\n\nOne of the greatest masters who showed this was Jesus Christ. There are three things people fear the most – death, pain and loss of wealth. Christ experienced two of these at the same time. He was tortured to death on the cross. He was experiencing pain, and he was dying. Yet even in that moment he had nothing but love. He made that famous statement on the cross – “Lord, forgive them, for they know not what they do.” Jesus Christ was a fully enlightened saint. He performed many miracles, and cou"

	sampleIAST = "5hotī hai. hameṃ patā hī nahīṃ rahā hai ki hṛdaya kahāṃ hai.\n\nhṛdaya se merā abhiprāya śārīrika hṛdaya se nahīṃ hai. use to hama jānate haiṃ. lekina śarīra-śāstrī aura vaidya-ḍākṭara kaheṃge ki usa hṛdaya meṃ prema kī saṃbhāvanā nahīṃ hai; vaha to kevala paṃpa kā kāma karatā hai, phuphphusa kā kāma karatā hai. usameṃ aura kucha nahīṃ hai. aura bāteṃ basa kapolakalpanā hai, kavitā hai, svapna haiṃ.\n\nlekina taṃtra jānatā hai ki tumhāre śārīrika hṛdaya ke pīche hī eka gaharā keṃdra chipā hai. usa gahare keṃdra taka mana ke dvārā hī pahuṃcā jā sakatā hai. kyoṃki hama mana meṃ haiṃ. hama apane mana meṃ haiṃ aura aṃtasa kī ora koī bhī yātrā vahīṃ se āraṃbha ho sakatī hai.\n\nmana dhvani hai. āvāja hai. agara saba dhvani baṃda ho jāe to tumhārā mana nahīṃ rahegā. mauna meṃ mana nahīṃ hai. yahī kāraṇa hai ki mauna para itanā bala diyā jātā hai. mauna a-mana avasthā hai. āmataura se hama kahate hai"

	sampleTables = "23\n\niti te kathitaṃ kānte svargaṣaṭkasya lakṣaṇam .\nyajjñānādamaratvañca jīvanmuktaśca sādhakaḥ ..13..\nyajjñānāñjananīgarbhaṃ na viśanti kadācana .\nāyurārogyamaiśvaryaṃ sa prāpnoti na saṃśayaḥ ..14..\n\npurāṇāni ca sarvāṇi mayaivoktāni pārvati .\netadrūpañca tanmadhye 'vyaktarūpo na vidyate ..15..\ngūḍhajñānañca tanmadhye ataḥ kiñcinna budhyate .\nevaṃ hi vedaśāstreṣu jñānamadhye sulocane ..16..\n\nśabdajñānaṃ yato nāsti ataḥ kiñcinna budhyate .\naṣṭādaśapurāṇāni sāṅgaṃ vetti ca yo naraḥ ..17..\ntasya sthāne purāṇānāṃ sadā śravaṇamācaret .\nmūḍhe cālpapāṭhajñe ca na śrotavyaṃ kadācana ..18..\n\nśāstrasya lakṣaṇaṃ hyetat vyākhyā cānyat prakāśate .\nśabdaḥ brahmasvarūpaśca mama vaktrād vinirgataḥ ..19..\nsandeho naiva kartavyo yadi muktiṃ prayacchati .\nsandehāt pāmaro yāti rauravaṃ pitṛbhiḥ saha ..20..\n\njñānaṃ ca nirmalaṃ kṛtvā buddhiṃ ca nirmalāṃ tataḥ .\nmahābhaktiyuto bhūtvā sarvaprāṇihite rataḥ ..21."

	sampleCipher = "�aymaeYYadymitglcRm etgytUaetgadymimlSane��laiFIaUoaElhotgIaYyblacleYYhSHraNha��toCYlgUlaymIaYydy lgIadhaFoClcatlUYyUyEYlS”TsOlytUa slamoicflaopaEo sIa slamlYpaymaElhotgaEo sa��toCYlgUlaetgaFoClcSa��slaoEmlcbeEYlaymayta sladytgSa��slate iclaopa slamlYpaymaFiclaeCecltlmmIaFiclaCy tlmmytUIaitepplf lgaEha slaFclmRltflaocaeEmltflaopa��toCYlgUlaocaYy��ytUSveblahoicaElytUaoi mygla symaEoghaopaEyc saetgagle saetgaeYYahoicaFcoEYldmaCyYYaElamoYblgSa��slhal��ym aElfeimlahoiaElYylblahoicmlYpaEocta oagylSa��tglflyblahoicmlYpaetgaElapcllSa��oiaeclato aeaFlcmotSa������\n������������������������������������������a��8wnpcnlcd8TwTdNldfuTwd3ssnwdPnCkpcgbyURIgCit2sdpCCdlNhnld3drnpwdcrpcdkwnnhuidkwuidhnlNwnldpshdNstCNspcNusldNldcrndkNwlcdtushNcNusdukdlnCk4wnpCN��pcNus9dvTcd3dkNshdcrndtushNcNusdNiHullNACndukdkTCkNCinsc9d3gsuwpstnducdusnlnCkdtpTlnldhnlNwnldpshdhnlNwnldHnwHncTpcndNgsuwpstn9d7dcwTC”dSNtNuTldtNwt"
)
