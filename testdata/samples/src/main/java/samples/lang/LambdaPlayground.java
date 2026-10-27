package samples.lang;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Optional;
import java.util.stream.Collectors;

/**
 * Lambda / method-reference fixtures for Defhop navigation.
 */
public class LambdaPlayground {
    private final List<Order> orders = new ArrayList<>();

    public List<String> orderIds() {
        // method reference -> Order.getId / field id
        return orders.stream().map(Order::getId).collect(Collectors.toList());
    }

    public List<Order> sortedByAmount() {
        return orders.stream()
                .sorted(Comparator.comparing(Order::getAmount))
                .collect(Collectors.toList());
    }

    public int totalActive() {
        return orders.stream()
                .filter(o -> o.getStatus() == Status.ACTIVE)
                .mapToInt(Order::getAmount)
                .sum();
    }

    public String labelOf(String id) {
        return Optional.ofNullable(find(id))
                .map(o -> {
                    String lambdaTag = o.getId() + ":" + o.getAmount();
                    return lambdaTag;
                })
                .orElseGet(() -> missingLabel(id));
    }

    public void forEachPrint() {
        orders.forEach(o -> {
            String lambdaLine = format(o);
            System.out.println(lambdaLine);
        });
    }

    public Order find(String id) {
        for (Order o : orders) {
            if (id.equals(o.getId())) {
                return o;
            }
        }
        return null;
    }

    private String format(Order o) {
        return o.getId() + "=" + o.getAmount();
    }

    private String missingLabel(String id) {
        return "missing:" + id;
    }
}
