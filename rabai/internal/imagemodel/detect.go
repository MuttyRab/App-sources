package imagemodel

import "strings"

// Family names, following stable-diffusion.cpp's model versions.
const (
	FamSD1          = "sd1"
	FamSD1Inpaint   = "sd1_inpaint"
	FamSD1Pix2Pix   = "sd1_pix2pix"
	FamSD1Tiny      = "sd1_tiny"
	FamSD2          = "sd2"
	FamSD2Inpaint   = "sd2_inpaint"
	FamSD2Tiny      = "sd2_tiny"
	FamSDXS512      = "sdxs_512"
	FamSDXS09       = "sdxs_09"
	FamSDXL         = "sdxl"
	FamSDXLInpaint  = "sdxl_inpaint"
	FamSDXLPix2Pix  = "sdxl_pix2pix"
	FamSDXLVega     = "sdxl_vega"
	FamSDXLSSD1B    = "sdxl_ssd1b"
	FamSVD          = "svd"
	FamSD3          = "sd3"
	FamFlux         = "flux"
	FamFluxFill     = "flux_fill"
	FamFluxControls = "flux_controls"
	FamFlex2        = "flex2"
	FamChroma       = "chroma"
	FamChromaRad    = "chroma_radiance"
	FamLongcat      = "longcat"
	FamFlux2        = "flux2"
	FamFlux2Klein   = "flux2_klein"
	FamFlux3        = "flux3"
	FamQwenImage    = "qwen_image"
	FamQwenLayered  = "qwen_image_layered"
	FamQwenImage21  = "qwen_image_2_1"
	FamZImage       = "z_image"
	FamZImageL2P    = "z_image_l2p"
	FamAnima        = "anima"
	FamOvis         = "ovis_image"
	FamErnie        = "ernie_image"
	FamLens         = "lens"
	FamMiniT2I      = "minit2i"
	FamPiD          = "pid"
	FamIdeogram4    = "ideogram4"
	FamSefi         = "sefi_image"
	FamKrea2        = "krea2"
	FamMageFlow     = "mage_flow"
	FamSenseNova    = "sensenova_u1_5"
	FamLLaDA        = "llada_image"
	FamPixArt       = "pixart"
	FamMing         = "ming_image"
	FamIris         = "iris"
	FamMoss         = "moss_image"
	FamFibo2        = "fibo2"
	FamHiDream      = "hidream_o1"
	FamBoogu        = "boogu_image"
	FamMiniMaxImg   = "minimax_h3_image"
	FamPH3          = "p_h3_image"
	// Video and video-upscale families: recognised so RabAI can say it only
	// makes images.
	FamWan       = "wan"
	FamHunyuan   = "hunyuan_video"
	FamLTX       = "ltx"
	FamMiniMaxH3 = "minimax_h3"
	FamLingbot   = "lingbot_video"
	FamRelayVSR  = "relay_vsr"
	FamSeedVR2   = "seedvr2"
)

// VideoFamilies make videos, which RabAI doesn't do.
var VideoFamilies = map[string]bool{FamWan: true, FamHunyuan: true, FamLTX: true, FamMiniMaxH3: true, FamLingbot: true, FamRelayVSR: true, FamSeedVR2: true, FamSVD: true}

// Family works out which image model family a file holds, or "" if it isn't
// a diffusion model. It is a port of ModelLoader::get_sd_version in
// stable-diffusion.cpp, run first on the names as stored (checkpoints) and
// then as the engine prefixes them when the file is given as a separate
// diffusion model.
func Family(f *File) string {
	if fam := familyOf(f.Tensors); fam != "" {
		return fam
	}
	prefixed := make(map[string][]int64, len(f.Tensors))
	for n, d := range f.Tensors {
		prefixed["model.diffusion_model."+n] = d
	}
	return familyOf(prefixed)
}

func familyOf(t map[string][]int64) string {
	has := func(n string) bool { _, ok := t[n]; return ok }
	ne := func(n string, i int) int64 {
		if d, ok := t[n]; ok && i < len(d) {
			return d[i]
		}
		return 0
	}
	for _, p := range []string{"", "model.diffusion_model.", "diffusion_model.", "transformer.", "unet.", "diffusion.", "wan."} {
		if (has(p+"positive_conditioning") || has(p+"prompt_embeds")) && (has(p+"lq_proj_in.conv1.weight") || has(p+"lq_proj.conv1.weight")) {
			return FamRelayVSR
		}
		if p != "wan." && has(p+"vid_in.proj.weight") && has(p+"blocks.0.attn.rope.rope.freqs") && has(p+"positive_conditioning") {
			return FamSeedVR2
		}
		if has(p+"model.x_embedder.proj1.weight") && has(p+"model.language_model.layers.0.self_attn.q_proj.weight") {
			return FamHiDream
		}
	}
	if has("model.diffusion_model.perceiver.latent_queries") {
		return FamFibo2
	}
	if has("model.diffusion_model.emb_in.image.weight") && has("model.diffusion_model.txt_mode_blocks.0.q_proj.weight") {
		return FamFlux3
	}
	if has("model.diffusion_model.video_embedder.weight") && has("model.diffusion_model.shared_block_modulation.weight") {
		return FamMoss
	}

	var (
		multiEnc, isUnet, isXL, isFlux, isFlux2, block47, isWan bool
		distilled, sdxlAdd, mid1, out311, out71, attn1024       bool
		tokenEmb, inputBlock, ctxEmb                            string
	)
	for name, dims := range t {
		c := func(s string) bool { return strings.Contains(name, s) }
		switch {
		case c("model.diffusion_model.double_blocks.") || c("model.diffusion_model.single_transformer_blocks."):
			isFlux = true
		}
		switch {
		case c("model.diffusion_model.net.lq_proj.latent_proj.0.weight"):
			return FamPiD
		case c("embed_image_indicator.weight"):
			return FamIdeogram4
		case c("model.diffusion_model.txtfusion.projector.weight") || c("model.diffusion_model.text_fusion.projector.weight"):
			return FamKrea2
		case c("model.diffusion_model.nerf_final_layer_conv."):
			return FamChromaRad
		case c("model.diffusion_model.joint_blocks."):
			return FamSD3
		case c("model.diffusion_model.transformer_blocks.0.attn.norm_added_q.weight") && has("model.diffusion_model.transformer_blocks.0.img_mlp.w1.weight"):
			return FamLens
		case c("net.img_embedder.proj1.weight"):
			return FamMiniT2I
		case c("language_model.model.layers.0.self_attn.q_proj_mot_gen.weight"):
			return FamSenseNova
		case name == "model.diffusion_model.txt_in.text_norm.weight":
			return FamQwenImage21
		case c("model.diffusion_model.transformer_blocks.0.img_mod.1.weight"):
			if ne("model.diffusion_model.img_in.weight", 0) == 128 {
				return FamMageFlow
			}
			if has("model.diffusion_model.time_text_embed.addition_t_embedding.weight") {
				return FamQwenLayered
			}
			return FamQwenImage
		case c("model.diffusion_model.txt_in.individual_token_refiner.blocks.0.adaLN_modulation.1.weight"):
			return FamHunyuan
		case c("llm_adapter.blocks.0.cross_attn.q_proj.weight"):
			return FamAnima
		case c("dual_time_embed.semantic_embedder.linear_1.weight"):
			return FamSefi
		case c("model.diffusion_model.double_blocks.0.img_mlp.gate_proj.weight"):
			return FamOvis
		case c("model.diffusion_model.sigvq_embedder.1.weight"):
			return FamLLaDA
		case c("model.diffusion_model.cap_embedder.0.weight"):
			if has("text_encoders.llm.connector.layers.0.self_attn.q_proj.weight") || has("text_encoders.connector.layers.0.self_attn.q_proj.weight") {
				return FamMing
			}
			if has("model.diffusion_model.local_decoder.out_conv.weight") {
				return FamZImageL2P
			}
			return FamZImage
		case c("double_stream_layers.0.img_instruct_attn.processor.img_to_q.weight"):
			return FamBoogu
		case c("model.diffusion_model.layers.0.adaLN_sa_ln.weight"):
			return FamErnie
		case c("model.diffusion_model.t_block.1.weight") && has("model.diffusion_model.x_embedder.proj.weight") && !has("model.diffusion_model.audio_patchify_proj.weight"):
			return FamPixArt
		case c("model.diffusion_model.adaln_single.emb.timestep_embedder.linear_1.bias"):
			if has("model.diffusion_model.pos_embed.proj.weight") && has("model.diffusion_model.adaln_single.linear.weight") && !has("model.diffusion_model.audio_patchify_proj.weight") {
				return FamPixArt
			}
			return FamLTX
		case c("model.diffusion_model.video_patch_proj.weight") && has("model.diffusion_model.condition_proj.weight"):
			if !has("model.diffusion_model.audio_patch_proj.weight") {
				return FamMiniMaxImg
			}
			dec := ne("first_stage_model.decoder.proj_out.weight", 1)
			if dec == 0 {
				dec = ne("vae.decoder.proj_out.weight", 1)
			}
			if dec == 768 {
				return FamPH3
			}
			return FamMiniMaxH3
		case name == "model.diffusion_model.y_embedder.layer_pool.weight" && has("model.diffusion_model.modulation_cores.adaln_img.weight"):
			return FamIris
		case c("model.diffusion_model.patch_embedder.weight"):
			return FamLingbot
		case c("model.diffusion_model.input_blocks.8.0.time_mixer.mix_factor"):
			return FamSVD
		}
		if c("distilled_guidance_layer.in_proj.weight") {
			distilled = true
		}
		if c("model.diffusion_model.double_stream_modulation_img.lin.weight") {
			isFlux2 = true
		}
		if c("single_blocks.47.linear1.weight") {
			block47 = true
		}
		if c("model.diffusion_model.blocks.0.cross_attn.norm_k.weight") {
			isWan = true
		}
		if c("model.diffusion_model.input_blocks.") || c("model.diffusion_model.down_blocks.") || c("unet.down_blocks.") {
			isUnet = true
			if multiEnc {
				isXL = true
			}
		}
		if c("conditioner.embedders.1") || c("cond_stage_model.1") || c("te.1") {
			multiEnc = true
			if isUnet {
				isXL = true
			}
		}
		if c("model.diffusion_model.add_embedding.") || c("model.diffusion_model.label_emb.0.0.") {
			sdxlAdd = true
		}
		if c("model.diffusion_model.middle_block.1.") || c("model.diffusion_model.mid_block.resnets.1.") || c("unet.mid_block.resnets.1.") {
			mid1 = true
		}
		if c("model.diffusion_model.output_blocks.3.1.transformer_blocks.1") || c("model.diffusion_model.up_blocks.1.attentions.0.transformer_blocks.1") || c("unet.up_blocks.1.attentions.0.transformer_blocks.1") {
			out311 = true
		}
		if c("model.diffusion_model.output_blocks.7.1") || c("model.diffusion_model.up_blocks.2.attentions.1") || c("unet.up_blocks.2.attentions.1") {
			out71 = true
			if c("model.diffusion_model.output_blocks.7.1.transformer_blocks.0.attn1.to_k.weight") && len(dims) > 0 && dims[0] == 1024 {
				attn1024 = true
			}
		}
		switch name {
		case "cond_stage_model.transformer.text_model.embeddings.token_embedding.weight",
			"cond_stage_model.model.token_embedding.weight",
			"text_model.embeddings.token_embedding.weight",
			"te.text_model.embeddings.token_embedding.weight",
			"conditioner.embedders.0.model.token_embedding.weight",
			"conditioner.embedders.0.transformer.text_model.embeddings.token_embedding.weight":
			tokenEmb = name
		case "model.diffusion_model.input_blocks.0.0.weight", "model.diffusion_model.conv_in.weight", "model.diffusion_model.img_in.weight", "unet.conv_in.weight":
			inputBlock = name
		case "model.diffusion_model.txt_in.weight", "model.diffusion_model.context_embedder.weight":
			ctxEmb = name
		}
	}
	_ = attn1024
	if isWan {
		return FamWan
	}
	inCh := ne(inputBlock, 2)
	inpaint, ip2p := inCh == 9, inCh == 8
	if isUnet && sdxlAdd {
		isXL = true
	}
	switch {
	case isXL:
		switch {
		case inpaint:
			return FamSDXLInpaint
		case ip2p:
			return FamSDXLPix2Pix
		case !mid1 && !out311:
			return FamSDXLVega
		case !mid1:
			return FamSDXLSSD1B
		}
		return FamSDXL
	case isFlux && !isFlux2:
		switch {
		case distilled:
			return FamChroma
		case ne(ctxEmb, 0) == 3584:
			return FamLongcat
		case ne(inputBlock, 0) == 384:
			return FamFluxFill
		case ne(inputBlock, 0) == 128:
			return FamFluxControls
		case ne(inputBlock, 0) == 196:
			return FamFlex2
		}
		return FamFlux
	case isFlux2:
		if block47 {
			return FamFlux2
		}
		return FamFlux2Klein
	}
	if !isUnet {
		return "" // a text encoder or VAE on its own, not a diffusion model
	}
	switch ne(tokenEmb, 0) {
	case 768:
		switch {
		case inpaint:
			return FamSD1Inpaint
		case ip2p:
			return FamSD1Pix2Pix
		case !mid1 && !out71:
			return FamSDXS512
		case !mid1:
			return FamSD1Tiny
		}
		return FamSD1
	case 1024:
		switch {
		case inpaint:
			return FamSD2Inpaint
		case !mid1 && attn1024:
			return FamSDXS09
		case !mid1:
			return FamSD2Tiny
		}
		return FamSD2
	}
	// A PyTorch .ckpt (shapes aren't read): the text encoder's name tells
	// SD 1.x (OpenAI CLIP) from SD 2.x (OpenCLIP).
	if isUnet && tokenEmb != "" && ne(tokenEmb, 0) == 0 {
		switch tokenEmb {
		case "cond_stage_model.transformer.text_model.embeddings.token_embedding.weight":
			return FamSD1
		case "cond_stage_model.model.token_embedding.weight", "conditioner.embedders.0.model.token_embedding.weight":
			return FamSD2
		}
	}
	return ""
}
