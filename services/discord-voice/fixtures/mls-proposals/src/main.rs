use openmls::prelude::{
    tls_codec::{DeserializeBytes, Serialize, VLBytes},
    BasicCredential, ExternalProposal, ExternalSender, GroupEpoch, GroupId, KeyPackageIn,
    LeafNodeIndex, OpenMlsProvider, ProtocolVersion, SenderExtensionIndex, SignatureScheme,
};
use openmls_basic_credential::SignatureKeyPair;
use openmls_rust_crypto::OpenMlsRustCrypto;
use std::{
    env, fs,
    io::{self, Write},
};

fn main() {
    let args: Vec<String> = env::args().skip(1).collect();
    let mut stdout = io::stdout().lock();
    let action = args[0].as_str();
    if action == "init" {
        let key = SignatureKeyPair::new(SignatureScheme::ECDSA_SECP256R1_SHA256).unwrap();
        fs::write(&args[1], key.tls_serialize_detached().unwrap()).unwrap();
        let sender = ExternalSender::new(
            key.public().into(),
            BasicCredential::new(b"swarm MLS fixture".to_vec()).into(),
        );
        stdout
            .write_all(&sender.tls_serialize_detached().unwrap())
            .unwrap();
        return;
    }

    let key = SignatureKeyPair::tls_deserialize_exact_bytes(&fs::read(&args[1]).unwrap()).unwrap();
    let group_id = GroupId::from_slice(&args[2].parse::<u64>().unwrap().to_be_bytes());
    let epoch = GroupEpoch::from(args[3].parse::<u64>().unwrap());
    let sender = SenderExtensionIndex::new(0);
    let proposal = match action {
        "add" => {
            let provider = OpenMlsRustCrypto::default();
            let member = KeyPackageIn::tls_deserialize_exact_bytes(&fs::read(&args[4]).unwrap())
                .unwrap()
                .validate(provider.crypto(), ProtocolVersion::Mls10)
                .unwrap();
            ExternalProposal::new_add::<OpenMlsRustCrypto>(member, group_id, epoch, &key, sender)
                .unwrap()
        }
        "remove" => {
            let leaf = LeafNodeIndex::new(args[4].parse::<u32>().unwrap());
            ExternalProposal::new_remove::<OpenMlsRustCrypto>(leaf, group_id, epoch, &key, sender)
                .unwrap()
        }
        _ => panic!("unknown fixture command"),
    };
    let proposals = VLBytes::from(proposal.tls_serialize_detached().unwrap());
    stdout
        .write_all(&proposals.tls_serialize_detached().unwrap())
        .unwrap();
}
