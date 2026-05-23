package domain

// TODO(KAC-147): factory-builders для domain-типов (evgeniy §D.7).
//
//   func NewLoadBalancer(projectID ProjectID, regionID RegionID, name LbName, ...) LoadBalancer { ... }
//   func NewListener(lbID ResourceID, ...) Listener { ... }
//   func NewTargetGroup(projectID ProjectID, regionID RegionID, ...) TargetGroup { ... }
//
// inline-литерал domain-сущности с magic-константами в use-case-слое запрещён —
// builders в domain-пакете (evgeniy §I.7-§I.8).
