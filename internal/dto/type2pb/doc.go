// Package type2pb — register'ы трансферов domain → proto через init().
//
// TODO(KAC-150): per-resource файлы (loadbalancer.go / listener.go / target_group.go /
// operation.go / timestamp.go) + RegTransfer в init() каждого файла.
//
// Тип-сет ограничивает компилятором допустимые пары:
//
//	type types2ProtoVariants interface {
//	    Perform() error
//	    *dto.DTO[domain.LoadBalancer, *lbv1.NetworkLoadBalancer] |
//	    *dto.DTO[domain.Listener, *lbv1.Listener] | ...
//	}
package type2pb
